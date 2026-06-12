# 02-1 — Knowledge Compiler (change #7)

Closes HIGH risk §2.1 of the backlog. Captured 2026-06-10.

---

## TL;DR

Change #7 introduces the `KnowledgeCompiler`: the **only writer
to `knowledge_objects`** (ADR-003) and the assembly point of the
7-worker pipeline. It encodes the 7 questions the spec requires
(per `proposal.md:205-219`) and performs an atomic 4-6 write
contract in a single `SERIALIZABLE` transaction.

The most architecturally significant gap is **`agreement_score`**
(ADR-002): one of the 5 inputs to `computed_confidence` is
currently external (provided by `RelationBuilder` or `Validator`).
The Compiler takes it as a stub. The follow-up to change #12
form closes the gap with a DB-derivable view.

**Eight concrete decisions**:

1. **New package `internal/compiler`** with
   `KnowledgeCompiler` interface and a `compile` function.
2. **`SERIALIZABLE` isolation** for the atomic write contract.
3. **The 7 questions are answered in order** (Q1 → Q7) as a
   deterministic internal pipeline. Q1-Q3 are the "existence
   decision"; Q4-Q6 are the "graph build"; Q7 is the
   "human-review" gate.
4. **`agreement_score` comes in via the `Validated` payload**
   from the worker pipeline. The Compiler is a **pure function
   of its declared inputs**; it does not invent it.
5. **4-6 atomic writes per compile**: `knowledge_objects` (1) +
   `lifecycle_events` (1) + `object_versions` (1) + `relations`
   (N) + `raw_inputs.status` UPDATE (1) + `audit_events` (1-3
   depending on Q6 and Q7).
6. **The duplicate path writes only**: `raw_inputs.status`
   UPDATE + `audit_events` (1 row, action
   `raw_input.duplicate_detected`). No `knowledge_objects`.
7. **`RequiresReview` flag** is part of `CompileResult` and
   triggers a Telegram notification via the outbox.
8. **The Compiler is the only writer to `knowledge_objects`**
   (ADR-003). Enforced at code review. The `KnowledgeObjectRepository`
   is not exposed to anything outside the compiler package.

---

## What change #7 needs to do

Per `tasks.md:66-71`, `proposal.md:194-227`, `design.md:269-409`:

- New `internal/compiler/` package.
- `Compile(raw_input) → {object_id, version, lifecycle_event, new_relations, rejected_reasons, requires_review}`.
- Encodes the 7 questions as a deterministic internal pipeline.
- The Compiler is the only writer to `knowledge_objects` (ADR-003).
- The atomic 4-6 write contract at `SERIALIZABLE` isolation.
- The `agreement_score` input is a stub from the worker
  pipeline. Formal DB-derivable definition is a follow-up after
  #12 lands.

---

## The 7 questions (per `proposal.md:205-219`)

The Compiler MUST answer:

1. **Does this already exist?** — dedup by
   `content_hash + workspace`. If yes, return the existing
   object.
2. **Is it a duplicate?** — if Q1 said yes, this is a duplicate.
   Mark `rejected_reasons = ['duplicate']`, write
   `raw_input.duplicate_detected` audit, return.
3. **What type is it?** — from the Classifier (`decision`,
   `research`, `architecture`, `task`, `idea`, `document`,
   `artifact`, `other`). The Classifier already ran in the
   worker pipeline.
4. **Does it contradict existing knowledge?** — compare to
   existing objects in the same workspace via the
   EntityExtractor output. If yes, emit `contradicts` relations
   and force `requires_review = true`.
5. **Should it merge with an existing object?** — if Q1
   candidate with high source overlap, same type, and
   `computed_confidence_for_new > 0.6`, merge instead of
   creating new.
6. **Should it create new relations?** — accept the
   `RelationsBuilt` value from the worker pipeline and persist
   each relation.
7. **Should it require human review?** — set
   `requires_review = true` if any:
   - `computed_confidence < 0.3`
   - Q4 detected a contradiction
   - `type = 'other'`
   - merge decision was ambiguous

The 7 questions are answered in order. Q1-Q3 are the "existence
decision" (does this become a new object?). Q4-Q6 are the
"graph build" (what relations?). Q7 is the "human review" gate.

---

## Sub-decisions inside change #7

### Decision 1: The `Compile` function signature

Per `design.md:324-350`:

```go
// internal/compiler/compiler.go
type KnowledgeCompiler interface {
    Compile(ctx context.Context, in RawInput) (CompileResult, error)
}

type RawInput struct {
    ID          uuid.UUID
    WorkspaceID uuid.UUID
    Source      SourceRef
    Content     string
    Metadata    map[string]any
    IdentityKey string
    ContentHash string
}

type CompileResult struct {
    ObjectID        uuid.UUID
    Version         int
    LifecycleEvent  LifecycleEvent
    NewRelations    []Relation
    RejectedReasons []string
    RequiresReview  bool
}
```

The function takes a `RawInput` (the inbox row + a bit of
context) and returns a `CompileResult`. The interface is
minimal; the implementation does the work.

**Question**: where does the `Validated` payload (with
`agreement_score`) come from? The Compiler doesn't read NATS.
The worker pipeline emits `raw_input.validated` events; a
subscriber compiles them.

**Decision**: a separate process (or goroutine in the API)
consumes `raw_input.validated` and calls `Compile`. The
`Validated` payload (with `agreement_score` from the
RelationBuilder or Validator) is passed as a parameter to
`Compile` via an extension to `RawInput` or as a separate
field. Since the spec says `Validated.agreement_score` is the
producer, the cleanest API is:

```go
type CompileInput struct {
    RawInput    RawInput
    Validated   ValidatedPayload  // includes agreement_score, contradictions, relations
}

type ValidatedPayload struct {
    Normalized      Normalized
    Classified      Classified
    Summarized      Summarized
    Extracted       Extracted
    RelationsBuilt  RelationsBuilt
    Validated       Validated
    AgreementScore  float64  // the stub: comes from RelationBuilder or Validator
}
```

The Compiler takes this entire input. It does NOT call the
workers itself. The workers ran before; their results are in
`ValidatedPayload`.

### Decision 2: Atomic write contract — isolation level

Per `design.md:356`, the atomic writes run at `SERIALIZABLE`
isolation. Why not `READ COMMITTED` (the default)?

- The Compiler reads `max(version)` and writes `version + 1`.
  Two concurrent compiles of the same `raw_input_id` would see
  the same `max(version)` and both write `version + 1`. The
  second write overwrites the first.
- With `SERIALIZABLE`, Postgres detects the conflict and
  retries one of them. The second compile sees the new version
  and increments correctly.

**Trade-off**: `SERIALIZABLE` has more overhead (predicate
locking, conflict detection) than `READ COMMITTED`. For a
write rate of 10/sec, this is fine. For 100/sec, the
serialization failures start to hurt.

**Decision**: `SERIALIZABLE` for the Compiler's transaction.
Conflict retries are bounded (max 3); on exhaustion, return a
"try again" error to the caller.

### Decision 3: The 4-6 atomic writes

Per `design.md:357-377`, the successful compile path writes:

1. **INSERT `knowledge_objects` row** (or UPDATE if merging)
   with `version = max(version) + 1`, `checksum = sha256(content
   || canonical_metadata || version)`.
2. **INSERT `lifecycle_events` row** with `from_status`,
   `to_status`, `actor`, `request_id`.
3. **INSERT `object_versions` row** with `content_snapshot`,
   `metadata`, `promoted_at`, `promoted_by`, `request_id`.
4. **INSERT `relations` rows** (one per Q6 relation, with
   non-empty `evidence`).
5. **UPDATE `raw_inputs.status` = 'compiled'** and
   `processed_at = now()`.
6. **INSERT `audit_events` rows** for:
   - `lifecycle.transitioned`
   - each `relation.created`
   - (if reached) `object.canonicalized`

Total: 4-6 writes depending on how many relations and audit
events. All in the same `SERIALIZABLE` transaction.

The duplicate path (`design.md:379-382`) writes only:
- `audit_events` row with action `raw_input.duplicate_detected`
- `UPDATE raw_inputs.status = 'rejected'`

No `knowledge_objects` row. No `object_versions`. No
`lifecycle_events`.

### Decision 4: Outbox writes (for §1.3)

The 3 event types from §1.3 + the audit events:

The 3 lifecycle events are written **inside the Compiler's
transaction** via `outbox.Record(ctx, tx, subject, payload, headers)`:

- `lifecycle.transitioned` — every successful compile.
- `relation.created` — one per Q6 relation.
- `object.canonicalized` — only if the new state is `canonical`.

The `audit_events` rows are written directly (they're
first-class table writes, not outbox-mediated). The outbox is
for **downstream consumer notifications**; the audit log is
**internal state**.

### Decision 5: `agreement_score` as a stub

Per ADR-002, `agreement_score` is provided by the
`RelationBuilder` (Worker 5) or `Validator` (Worker 6). The
Compiler takes it via `ValidatedPayload.AgreementScore`.

The Compiler **does not validate** this value (it must be in
[0, 1], but the producer is supposed to ensure that). It just
plugs it into the `computed_confidence` formula.

**Post-change-#12 follow-up** (per `design.md:784-788`): when
the `v_object_agreement_score` SQL view exists, the Compiler
queries it and replaces the stub. The `ValidatedPayload`
becomes a query result, not a worker-pushed value.

### Decision 6: The "only writer" rule (ADR-003)

Per `design.md:794-830` (ADR-003):

> "The Compiler is the only writer to `knowledge_objects`."

How to enforce:
- **At the type level**: the `KnowledgeObjectRepository.Insert`
  and `KnowledgeObjectRepository.Update` methods are exported.
  The Compiler uses them. Anything else that wants to write
  must either go through the Compiler or call these methods
  directly.
- **At code review**: a non-Compiler call to `Insert` or
  `Update` is rejected.
- **At the test level**: a test asserts that all callers of
  `KnowledgeObjectRepository.Insert` and `.Update` are in the
  `compiler` package. (Or use a Go analyzer / `go vet` rule.)

**Practical enforcement**: the `Insert` and `Update` methods
are in the `postgres` package, exported. The Compiler uses
them. Other code paths (e.g., `IngestTextService` after
change #5) do NOT call them — they go through the Compiler.

**Risk**: an agent or future code could call `Insert` directly.
Mitigation: code review + a smoke test that grep's the codebase
for `KnowledgeObjects().Create` and `KnowledgeObjects().Update`
and asserts all callsites are in `internal/compiler/`.

### Decision 7: Idempotency

The Compiler is **idempotent on `raw_input_id`**. Replaying a
compile (e.g., a redelivered NATS message) is safe because:

- The `raw_inputs` row already exists. The Compiler's transaction
  begins with `SELECT FROM raw_inputs WHERE id = $1`.
- If `status = 'compiled'`, the Compiler returns the existing
  result without writing again. The second compile is a
  no-op.
- If `status = 'pending'`, the Compiler proceeds. The
  transaction commits. A second compile (concurrent) sees
  `status = 'compiled'` (after the first commits) and no-ops.

The worker's idempotency is on `(raw_input_id, worker_name)`
(per `knowledge-pipeline/spec.md:196-204`). The Compiler's
idempotency is on `raw_input_id` alone.

**Implementation**: the Compiler's first action is
`SELECT status FROM raw_inputs WHERE id = $1 FOR UPDATE`. If
the status is `compiled`, return the cached result. This is a
cheap early-exit.

### Decision 8: Concurrency / ordering

The Compiler processes one `raw_input_id` at a time. There's
no global lock; the `raw_inputs.status` update is the per-
row mutex.

Multiple Compiler instances can run in parallel (NATS
consumers, multiple replicas). Postgres handles the
serialization via `SERIALIZABLE` isolation + per-row status
updates.

Cross-`raw_input_id` ordering doesn't matter — different raw
inputs can compile in parallel. Same-`raw_input_id` ordering
is enforced by the `SELECT ... FOR UPDATE` and the
`raw_inputs.status` check.

---

## Where `agreement_score` comes from (ADR-002)

The most architecturally significant gap. Per `design.md:756-792`:

- `computed_confidence` has 5 inputs: `source_count`,
  `age_days`, `lifecycle_state`, `contradicting_relation_count`,
  `agreement_score`.
- 4 of 5 are DB-derivable today.
- `agreement_score` is **provided externally** by the
  `RelationBuilder` or `Validator` worker.
- The Compiler is a pure function of its declared inputs. The
  debt is visible (ADR-002 documents it).

**Candidate derivation** (per `design.md:670-672`):

```sql
agreement_score = 1 - (contradicting_relations / total_relations)
                  when total_relations > 0
                = 1.0
                  when total_relations = 0
```

**Decision**: this derivation lands as a follow-up to
change #12 (`relations-bidirectional-and-freshness`). The
Compiler receives `agreement_score` from the worker pipeline
until then. The follow-up is ~½ day: create the SQL view
`v_object_agreement_score`, update the Compiler to query it,
update ADR-002 to "debt paid".

---

## What goes in change #7

1. **New package `internal/compiler/`** with:
   - `compiler.go` — the `KnowledgeCompiler` interface + impl.
   - `compile.go` — the `Compile` function with the 7 questions.
   - `atomic_writes.go` — the SERIALIZABLE transaction.
   - `validation.go` — the `ValidatedPayload` parsing.
   - `errors.go` — typed errors for "duplicate", "requires review",
     "merge conflict", etc.
2. **New port `internal/app/compiler.go`** for the `KnowledgeCompiler`
   interface (so other packages can depend on the port).
3. **Update `cmd/api/main.go`** to wire the Compiler (after
   `KnowledgeCompiler.Compile` exists, the API can enqueue
   compiles via the inbox).
4. **Update `cmd/drainer/main.go`** (change #8) — the
   drainer publishes `raw_input.validated` events; a
   subscriber consumes them and calls `Compile`. **Or** the
   compile subscription is in a separate `cmd/compiler/`
   process. Decision below.
5. **Refactor `IngestTextService`** to NOT write to
   `knowledge_objects` directly (after change #5 + #7, it
   only writes to `raw_inputs` + `audit_events` + the
   `pending_events` outbox).
6. **Tests**:
   - `internal/compiler/compile_test.go` — 7 questions, all
     paths (new, merge, duplicate, reject, requires_review).
   - `internal/compiler/atomic_writes_test.go` — SERIALIZABLE
     conflict retries.
   - `internal/compiler/idempotency_test.go` — replay safety.
   - Integration test: ingest → inbox → 7 workers → compile
     → `knowledge_objects` row exists.
7. **Update `design.md`** to remove the `agreement_score`
   external input from §3.4 (when the follow-up lands).
8. **Update `tasks.md`** to mark #7 done.

### Compile process: where does it run?

| Option | Pros | Cons |
|---|---|---|
| **A. `cmd/compiler/main.go` (separate process)** | Independent scaling; failure isolation | Another process to operate |
| B. In the API process | One less process | Couples API availability to compile; bad for bursty loads |
| C. In the drainer process | One less process | Conflates two concerns |

→ **A — `cmd/compiler/main.go`**. The compile is a long-
running consumer of `raw_input.validated` events. It deserves
its own process, its own pgx pool, its own scaling. Same
pattern as the drainer (change #8).

The compile process:
1. Subscribes to `raw_input.validated` on NATS JetStream.
2. For each message, runs the 7-question pipeline.
3. Performs the atomic write contract.
4. Acks the message.
5. On failure, NACKs (handled by the §1.2 NACK semantics).

---

## What this is NOT

- **Not the 7 workers**. The workers (Normalizer, Classifier,
  etc.) are existing or future changes. The Compiler consumes
  their output; it does not run them.
- **Not the inbox refactor**. That's change #5. This change
  assumes #5 is in place.
- **Not a refactor of `IngestTextService`**. That's change #5.
  This change consumes the new `raw_inputs` row.
- **Not a graph DB**. We stay in Postgres.
- **Not the `agreement_score` formalization**. That's the
  follow-up to change #12.

---

## Risks and edge cases

### Risk 1: SERIALIZABLE conflict storms

`SERIALIZABLE` causes conflicts under high concurrency. At
10 compiles/sec on the same `raw_input_id`, every other one
fails and retries. At 1 compile/sec on different
`raw_input_id`s, no conflicts.

Mitigation:
- Bound the retries (max 3).
- On retry exhaustion, return "try again" error. The caller
  (NATS consumer) re-queues.
- For bursty loads, the per-row lock (`SELECT FOR UPDATE`)
  plus the `raw_inputs.status` check is enough for most
  workloads.

### Risk 2: `agreement_score` external

The Compiler is **not** a pure function of DB state today. It
takes `agreement_score` from the worker pipeline. This breaks
the "reproducible from DB" property of `computed_confidence`.

Mitigation: the ADR-002 follow-up after #12. Until then, the
audit log records who provided `agreement_score` (the
RelationBuilder or Validator), so we can re-derive later.

### Risk 3: 4-write contract drift

The "only 4 writes" assertion is the load-bearing invariant
of the old codebase. After change #5, the contract becomes
"3 writes at ingest + 4-6 at compile" (different boundary).

Mitigation: the renamed test (`TestIngestHasNoDeferredExternalDependencies`,
per §1.4) asserts the principle, not the count. The
compiler has its own test (`TestCompilerAtomicWriteContract`)
that asserts the count is 4-6 (with the relations variable
size).

### Risk 4: `RequiresReview` notification

The Compiler sets `requires_review = true` for some compiles.
Today, no one is notified. The Telegram validation UI (change
#14) is the consumer.

Mitigation: the outbox event `object.requires_review` (or
similar) is published. The Telegram bot consumes it and
notifies a human.

### Risk 5: Versioning collisions

`max(version) + 1` works under `SERIALIZABLE`. Under
`READ COMMITTED`, two concurrent compiles of the same object
could both see `max = 3` and both write `version = 4`. The
second would either fail (UNIQUE constraint) or overwrite.

Mitigation: the migration for change #3 (versioning-and-versions-table)
adds a UNIQUE constraint on `(object_id, version)`. Combined
with `SERIALIZABLE`, the system is safe.

### Risk 6: Long-running compile

A compile that takes 30+ seconds (large relations, large
audit batch) holds a SERIALIZABLE transaction. Other
operations on the same `raw_input_id` are blocked.

Mitigation: the relations set is bounded (typically < 20
relations per compile). The audit batch is bounded. The
compile should complete in < 1 second for normal cases. If
it takes longer, the SERIALIZABLE transaction is too heavy
and we should consider `READ COMMITTED` + application-level
optimistic locking (version field).

### Risk 7: Cascade from inbox failure

If the inbox (`raw_inputs` table) is slow, the compile is
slow. The compile reads `raw_inputs.status` at the start of
the transaction. If the inbox has lock contention, the
compile blocks.

Mitigation: separate pgx pools for the inbox (change #5) and
the compiler (change #7). No shared contention.

### Risk 8: Memory in `compile.go`

The `ValidatedPayload` is large (entities, relations,
summaries, all the intermediate work). Holding it in memory
during the transaction is fine for typical sizes (a few KB)
but could be problematic for very large inputs.

Mitigation: stream the entities/relations from the worker
output instead of materializing the full payload. Or limit
the payload size in the worker contract.

---

## Connection to other changes

| Change | Connection |
|---|---|
| #1 workspace-id-uuid-migration | Prerequisite. The Compiler uses UUID workspace_id. |
| #2 lifecycle-states-migration | Prerequisite. The Compiler writes 7-state lifecycle events. |
| #3 versioning-and-versions-table | Prerequisite. The Compiler writes `object_versions` rows. |
| #4 freshness-and-owner | Prerequisite. The Compiler sets `next_review_at` and `owner_id`. |
| #5 raw-inputs-inbox | Prerequisite. The Compiler reads from `raw_inputs`. |
| #6 lifecycle-events-log | Prerequisite. The Compiler writes `lifecycle_events` rows. |
| #8 event-bus-nats | Prerequisite. The Compiler consumes `raw_input.validated` via NATS. The `cmd/compiler` process is structured like the drainer. |
| #9 agents-shared-brain | **Direct**: the agents call `Compile` via the runtime. The Compiler is the substrate for all agents. |
| #10 hybrid-retrieval-wiring | Independent. |
| #11 hnsw-embedding-index | Independent. |
| #12 relations-bidirectional-and-freshness | **After**: enables the `agreement_score` follow-up. |
| #14 telegram-validation-ui | Consumes `object.requires_review` events. |

The `agreement_score` follow-up (after #12) closes ADR-002
and makes the Compiler a pure function of DB state.

---

## What this DOES enable

- **All 7 workers have a consumer**: the Compiler is the
  endpoint of the pipeline. Workers emit `raw_input.validated`
  → Compiler compiles → `knowledge_objects` row exists.
- **All agents have a single write surface**: the runtime
  (change #9) calls `Compile` for every agent's write.
  Research, Architect, Documentation, etc. all use the
  same Compiler.
- **The `agreement_score` follow-up** can be specified.
- **Change #14 (Telegram validation UI)** has an event
  source: `object.requires_review`.
- **Hybrid retrieval** is complete: FTS, vector, structured,
  graph (from #12). The Compiler produces the data the
  retrieval reads.

---

## Spec anchors

- `openspec/changes/paradigm-knowledge-os/proposal.md:194-227` — Compile function, 7 questions
- `openspec/changes/paradigm-knowledge-os/design.md:269-409` — Compile design, atomic writes, agreement_score
- `openspec/changes/paradigm-knowledge-os/design.md:756-792` — ADR-002 (agreement_score)
- `openspec/changes/paradigm-knowledge-os/design.md:794-830` — ADR-003 (only writer)
- `openspec/changes/paradigm-knowledge-os/tasks.md:66-71` — change #7 task
- `openspec/changes/paradigm-knowledge-os/exploration.md:518-528` — exploration
- `research/01-1-confidence-freshness-model.md` — confidence formula
- `research/01-2-nats-nack-semantics.md` — NACK for compile consumer
- `research/01-3-transactional-outbox.md` — outbox for compile events
- `research/01-4-4-write-contract-test.md` — test principle (now 4-6 writes, not 4)
- `research/02-2-event-bus-nats.md` — bus substrate
- `research/02-4-relations-bidirectional-freshness.md` — relations substrate + agreement_score follow-up
