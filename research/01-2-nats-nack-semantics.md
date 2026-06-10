# 01-2 — NATS NACK Semantics (change #8 `event-bus-nats`)

Captured from working session 2026-06-10. Grounded in the
official NATS JetStream docs and the project's
`openspec/changes/paradigm-knowledge-os/{proposal,design}.md`.

---

## TL;DR

Adopt the backlog default for change #8 (`event-bus-nats`) with
five concrete refinements:

1. **JetStream at-least-once** + idempotency-on-`raw_input_id` (already
   in the spec) gives us **effective exactly-once** without paying the
   exactly-once tax.
2. **Retry timing via per-consumer `BackOff` array**, not app-level
   sleeps. Server-side, deterministic, no client code required.
3. **`MaxDeliver = 5`** per consumer, with a small **per-worker
   exception** (LLM workers may need `MaxDeliver = 3` because retries
   cost money; in-process workers can go higher).
4. **DLQ via NATS advisory subscription**, not a built-in queue.
   Subscribe to `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<consumer>`
   and republish to `{subject}.dlq` with the original payload.
5. **Workers can `Term()` early for obvious poison** (parse errors,
   schema violations) — no need to burn 5 retries on something that
   will never succeed.

---

## The defaults in the backlog

From `research/paradigm-knowledge-os-backlog.md` §1.2:

> Default recomendado: JetStream at-least-once, backoff exponencial,
> cap 5, dead-letter `{subject}.dlq`.

The four pieces map to NATS primitives as follows:

| Backlog default       | NATS primitive                                                                 |
|-----------------------|--------------------------------------------------------------------------------|
| JetStream at-least-once | `Stream` + `Consumer` with `AckPolicy: explicit` and `MaxAckPending: N`       |
| Exponential backoff   | `BackOff []time.Duration` on the Consumer config (server-driven retry timing)  |
| Cap 5                 | `MaxDeliver: 5` on the Consumer config                                          |
| Dead-letter `{subject}.dlq` | Subscribe to `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<consumer>` and republish to `{subject}.dlq` |

**Important**: DLQ in NATS is **not a first-class construct** like SQS
or Kafka. The "official" pattern is the `MAX_DELIVERIES` advisory +
client-side republish. This is a well-known pattern; the NATS docs
document it explicitly.

---

## The 5 refinements

### Refinement 1 — At-least-once + idempotency = effective exactly-once

The pipeline spec (`knowledge-pipeline/spec.md:186-204`) already
mandates that every worker is keyed on `raw_input_id` and short-
circuits on `(raw_input_id, worker_name)`. This means **a redelivered
message will not double-write**.

That property converts at-least-once into effective exactly-once
**without paying for transactional message production**. The
trade-off: we accept the small operational cost of idempotency keys
in the DB (which we already pay for other reasons).

**Don't enable JetStream's `exactly_once` delivery mode**. It's
expensive (deduplication window, stream coordination) and buys us
nothing the application-level idempotency isn't already giving us.

### Refinement 2 — Use `BackOff` array, not app-level sleeps

NATS supports two ways to time a retry:

- **`BackOff []time.Duration`** on the Consumer config: the server
  uses this sequence instead of `AckWait` for each redelivery.
  Deterministic. No client code.
- **`NakWithDelay(d)`** at the worker: the worker decides per
  failure. Useful for "I need to wait for a circuit breaker".

**Default**: configure `BackOff` per consumer. The array becomes the
retry policy. Workers call `Nak()` and the server honors the timing.

A reasonable LLM-friendly default (for workers that call external
APIs): `[1s, 5s, 30s, 2m, 10m]` — 5 attempts, ~12 minutes total
wait. For in-process workers, `[100ms, 500ms, 2s, 5s, 10s]` is
enough.

### Refinement 3 — `MaxDeliver = 5`, with per-worker overrides

Cap 5 retries (backlog default) is the right number for our
throughput. But **not all workers are equal**:

| Worker             | Suggested `MaxDeliver` | Rationale                                   |
|--------------------|------------------------|---------------------------------------------|
| Normalizer         | 5                      | Pure in-process, retries are cheap          |
| Classifier         | 3                      | LLM call; retries cost money                |
| Summarizer         | 3                      | LLM call; retries cost money                |
| EntityExtractor    | 3                      | LLM call                                    |
| RelationBuilder    | 5                      | Mostly in-process, but LLM for some         |
| Validator          | 5                      | In-process                                  |
| Compiler           | 5                      | In-process + DB                             |

**The cap is per-consumer, not per-stream.** Each worker's
subscription has its own consumer config.

### Refinement 4 — DLQ via `MAX_DELIVERIES` advisory + republish

Pattern:

1. Configure a **DLQ-watcher** subscription on
   `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<consumer>`
   for every stream/consumer pair.
2. The watcher extracts the original message (advisory payload
   contains the stream sequence number; pull the original from the
   stream).
3. The watcher republishes to `{subject}.dlq` with metadata
   (original subject, consumer name, `raw_input_id`, last error if
   available).
4. A separate DLQ-consumer (out of scope for change #8, but planned
   for a follow-up) handles the DLQ — alert, log, manual replay,
   or discard.

**Naming convention** for DLQ subjects: `{subject}.dlq` (per backlog
default). Examples:
- `raw_input.received.dlq`
- `lifecycle.transitioned.dlq`
- `relation.created.dlq`
- `object.canonicalized.dlq`

### Refinement 5 — Workers can `Term()` for obvious poison

A worker that detects a non-retriable error (parse failure, schema
violation, type assertion failure) should call `Term()` immediately
— not `Nak()`. This:

- Skips the 5-retry waste.
- Routes the message directly to the DLQ advisory flow.
- Carries a `reason` string via `TermWithReason(reason)` (NATS server
  ≥ 2.10.4), which lands in the advisory payload.

Pattern in Go:

```go
if err := parsePayload(msg.Data); err != nil {
    msg.TermWithReason("payload_parse_error: " + err.Error())
    return
}
```

This is **the only app-level retry decision** a worker should make:
"is this a transient failure (Nak) or a permanent failure (Term)?"

---

## Connection management (addresses backlog risk §2.2)

The backlog flags that NATS subscriptions must not block shutdown,
and that the NATS client + pgx connection pool need careful
coordination. Concrete rules:

- **Drain on shutdown**: every NATS subscription calls `Drain()` on
  `SIGTERM`/`SIGINT`. Drain stops accepting new messages, lets
  in-flight messages complete, and acks them.
- **Context-based cancellation**: every long-running consumer loop
  has a `ctx.Context`. When the context is cancelled, the loop
  exits.
- **pgx pool close AFTER NATS drain**: order matters. If we close
  pgx first, in-flight worker transactions fail mid-ack and the
  message gets redelivered. The right order:
  1. Stop accepting new HTTP requests.
  2. Drain NATS subscriptions.
  3. Close pgx pool.
  4. Exit.
- **Reconnect with jitter**: NATS client supports
  `nats.CustomReconnectDelay`. For our scale, the default is fine;
  flag this as a tunable.

---

## What the spec already commits to

From `design.md:120-136` (§2.2 NATS — Event Bus):

- Subjects: `raw_input.received`, `lifecycle.transitioned`,
  `relation.created`, `object.canonicalized`.
- Idempotency Key: NATS message ID + payload `raw_input_id`.
- Ordering: per-subject FIFO with a single stream consumer per
  worker.
- Failure modes: "Worker NACK: bus retries with backoff (cap retries
  in `event-bus-nats` chained change)."

From `design.md:582-588` (Kafka rejected):

- NATS is the chosen bus. The reasoning is solid and doesn't need
  to be revisited.

From `proposal.md:277-282` (processing pipeline):

- Workers are independent and replaceable.
- Idempotency: keyed on `raw_input_id`.
- Ordering: per `raw_input_id`.

---

## What needs to go into change #8

The change spec should commit to:

1. **Stream config** for each of the 4 subjects:
   - `Storage: file` (durable, survives restart)
   - `Retention: Limits`
   - `MaxAge: 7 days` (the 4 subjects are not audit-of-record; raw
     rows in the DB are)
   - `MaxMsgs: 1_000_000` per stream
   - `MaxBytes: 10 GB` per stream
2. **Consumer config per worker** (see refinement 3 table):
   - `Durable: <worker_name>`
   - `AckPolicy: Explicit`
   - `AckWait: 90s` (LLM calls can take 30s+; 90s is comfortable)
   - `MaxAckPending: 10` (bounded concurrency per worker)
   - `BackOff: [1s, 5s, 30s, 2m, 10m]` (LLM workers) or tighter for
     in-process workers
   - `MaxDeliver: 5` (or 3 for LLM workers, per refinement 3)
3. **DLQ-watcher service** that subscribes to all 4 streams'
   `MAX_DELIVERIES` advisories and republishes to `{subject}.dlq`.
4. **Shutdown ordering** documented (refinement "Connection
   management").
5. **Test plan**:
   - NAK with retry exhaustion routes to DLQ
   - `Term()` routes to DLQ without retries
   - Worker crash mid-ack causes redelivery, idempotency makes it
     safe
   - NATS drain on SIGTERM completes in-flight messages before
     returning

---

## Stream storage and retention (clarification)

The backlog says "JetStream at-least-once" but doesn't specify
storage. Recommendation:

- **File storage** for all 4 streams. The bus is part of the
  audit-trail surface; volatile storage would lose recent events on
  restart.
- **Retention: Limits**, not `WorkQueue`. We want the stream to keep
  events for replay/debug for a bounded window, then drop them.
  `WorkQueue` would drop on ack, which defeats post-mortem
  inspection.

A reasonable default:
- `MaxAge: 7 days`
- `MaxMsgs: 1_000_000` per stream
- `MaxBytes: 10 GB` per stream

Tune in production based on actual throughput.

---

## What this is NOT

- **Not a queue per subject** — NATS subjects are pub/sub topics,
  not queues. The queue semantics come from the `Consumer` (durable
  pull consumer) and the `MaxAckPending` bound.
- **Not competing consumers across worker types** — each worker
  subscribes to its own `Durable` consumer. Two Normalizer replicas
  can compete; a Normalizer and a Classifier don't compete (they
  subscribe to different subjects or different filter subjects).
- **Not a replacement for the DB transaction** — workers write to
  the DB within a transaction. The bus message is the *signal*,
  not the *write*. (See backlog §1.3 for the outbox discussion.)

---

## Risks not addressed by this analysis

These are out of scope for §1.2 but worth tracking:

- **pgx connection pool exhaustion under burst load** — if 10
  workers each grab 10 messages and each message needs 2 DB
  connections, we need a pool of ≥ 20 just for in-flight work.
  Tune `MaxAckPending` and pgx pool size together.
- **Subject explosion** — if we add a new event type per object
  type, the subject space grows. Use generic subjects + payload
  typing, not per-type subjects.
- **Clock skew** — `NakWithDelay` and `BackOff` are server-time.
  If clients have wildly skewed clocks, retries are unaffected
  (server is the source of truth). Good.
- **Stream compaction** — currently `Limits`. If we want a full
  audit log, that's a different stream with `Retention: Limits` +
  `Discard: old` and longer `MaxAge`. Not needed for change #8.

---

## Spec anchors

- `proposal.md:259-282` — processing pipeline + NATS subjects
- `design.md:120-136` — §2.2 NATS Event Bus
- `design.md:582-588` — Kafka rejection rationale
- `design.md:854-862` — original NACK open question
- `tasks.md:73-79` — change #8 description
- `specs/knowledge-pipeline/spec.md:138-204` — 7-worker contract
- NATS docs: `nats-concepts/jetstream/consumers.md`,
  `using-nats/developing-with-nats/js/consumers.md`
- nats.go: `jetstream/consumer_config.go`,
  `jetstream/message.go` (`TermWithReason`)
