# 01-4 — 4-Write Contract Test Reframe (change #5 `raw-inputs-inbox`)

Captured from working session 2026-06-10. Closes backlog §1.4.

---

## TL;DR

The renamed test `TestIngestHasNoDeferredExternalDependencies`
asserts the **principle** of dependency-free ingest, not the
**count** of writes. The new test body has 5 layers of assertion:

1. **Behavioral**: ingest completes successfully when LLM, NATS,
   and embedder are unavailable.
2. **Persistence**: `raw_inputs` + `audit_events` +
   `pending_events` rows exist after ingest.
3. **Response**: returns a valid `raw_input_id` (or duplicate
   marker), no error.
4. **Latency**: bounded (e.g., < 100 ms) — catches hidden
   external calls.
5. **No goroutine leaks**: `goleak.VerifyNone(t)` after.

The test uses **panicking dependencies** — fake LLM, NATS
publisher, and embedder that panic if called. If any future refactor
of `IngestTextService` accidentally calls one of them, the test
fails loudly. This is stronger than mocking with call recorders
(catches the bug at the call site, not at the assertion).

Plus three **focused sibling tests** for granular coverage:
- `TestIngestDoesNotCallLLM`
- `TestIngestDoesNotPublishToNATS`
- `TestIngestDoesNotCallEmbedder`

---

## What the 4-write contract was, and what it becomes

### Old paradigm

`IngestTextService.Ingest()` wrote **4 records** in a single
transaction (`internal/app/ingest_text.go:147-158`):

1. `sources` row
2. `knowledge_objects` row
3. `object_sources` (link) row
4. `audit_events` row

The current test `TestIngestDoesNotRequireDeferredExternalCapabilities`
(in `internal/app/ingest_text_test.go:130-154`) asserts that these
4 writes happen with `logger = nil` and no Telegram/RAG
dependencies.

### New paradigm (after change #5 + #8)

Per `proposal.md:221-226`, the contract is **reframed, not
deleted**:

- **Ingest** writes 3 records: `raw_inputs` + `audit_events` +
  `pending_events` (the outbox — see `01-3`).
- **Compiler** (later) writes 4 records: `knowledge_objects` +
  `lifecycle_events` + `object_versions` + `relations`.
- **Total per pipeline run**: 7 records, but the **ingest
  boundary** is now 3 writes.

The "4-write" count is a **historical accident** of the old
code. The **principle** the test is protecting is "ingest has no
external dependencies" — and that principle survives the rename.

---

## Why the rename

The old name `TestIngestDoesNotRequireDeferredExternalCapabilities`
is technically correct but reads as a double negative ("does not
require ... not having"). The new name
`TestIngestHasNoDeferredExternalDependencies` is direct, says the
same thing, and reads cleanly in CI logs and test output.

This is a renaming of a load-bearing invariant. The
**principle** must survive intact; only the assertion shape needs
to adapt to the new write count (3, not 4).

---

## The recommended test body

```go
func TestIngestHasNoDeferredExternalDependencies(t *testing.T) {
    // 1. Setup: dependencies that PANIC if called.
    llm       := &panickingLLMClient{}
    natsPub   := &panickingNATSPublisher{}
    embedder  := &panickingEmbedder{}
    
    // 2. Build the service with the live (panicking) dependencies.
    //    If the production wiring is correct, the constructor takes
    //    these as interfaces. In tests we inject the panicking fakes.
    svc := NewIngestTextServiceWithDeps(IngestTextDeps{
        UoW:       newFakeUOW(),
        IDs:       fixedIDs{...}.next,
        Now:       func() time.Time { return time.Date(...) },
        Logger:    testLogger(t),
        LLM:       llm,        // panics if .Complete() is called
        NATS:      natsPub,    // panics if .Publish() is called
        Embedder:  embedder,   // panics if .Embed() is called
    })
    
    // 3. Run the test under a goroutine-leak detector.
    defer goleak.VerifyNone(t)
    
    // 4. When: ingest.
    start := time.Now()
    result, err := svc.Ingest(context.Background(), domain.IngestTextRequest{
        WorkspaceID: "workspace-1",
        Content:     "text-only knowledge",
    })
    elapsed := time.Since(start)
    
    // 5. Then: success.
    require.NoError(t, err)
    require.NotEmpty(t, result.RawInputID)
    require.False(t, result.Duplicate)
    
    // 6. And: persistence — 3 rows, not 4.
    require.Equal(t, 1, uow.repos.rawInputs.createdCount())
    require.Equal(t, 1, uow.repos.auditEvents.createdCount())
    require.Equal(t, 1, uow.repos.pendingEvents.createdCount())
    
    // 7. And: latency bounded (no hidden external call).
    require.Less(t, elapsed, 100*time.Millisecond,
        "ingest took %s; expected < 100ms with all external deps panicking", elapsed)
}
```

The panicking fakes are the load-bearing trick. They make any
accidental external call **loud**: the test panics at the call
site, not at the end. This is much stronger than a mock that
records calls and asserts "0 calls" at the end.

```go
type panickingLLMClient struct{}

func (p *panickingLLMClient) Complete(_ context.Context, _ string) (string, error) {
    panic("IngestTextService must not call the LLM; that is the Classifier's job")
}
```

Same shape for `panickingNATSPublisher` and `panickingEmbedder`.

---

## The three focused sibling tests

The canonical test asserts the **principle** with all three
dependencies at once. Three smaller tests give granular coverage
and better failure messages:

```go
func TestIngestDoesNotCallLLM(t *testing.T) {
    recorder := &callRecorderLLM{}
    svc := NewIngestTextServiceWithDeps(IngestTextDeps{..., LLM: recorder})
    
    _, err := svc.Ingest(ctx, req)
    require.NoError(t, err)
    require.Equal(t, 0, recorder.calls, "ingest must not call the LLM")
}

func TestIngestDoesNotPublishToNATS(t *testing.T) {
    recorder := &callRecorderNATS{}
    svc := NewIngestTextServiceWithDeps(IngestTextDeps{..., NATS: recorder})
    
    _, err := svc.Ingest(ctx, req)
    require.NoError(t, err)
    require.Equal(t, 0, recorder.calls, "ingest must not publish to NATS")
}

func TestIngestDoesNotCallEmbedder(t *testing.T) {
    recorder := &callRecorderEmbedder{}
    svc := NewIngestTextServiceWithDeps(IngestTextDeps{..., Embedder: recorder})
    
    _, err := svc.Ingest(ctx, req)
    require.NoError(t, err)
    require.Equal(t, 0, recorder.calls, "ingest must not call the embedder")
}
```

Use a **call recorder** here (not a panicking fake) so a regression
shows up as a clean test failure with the call count, not a stack
trace from the panic.

---

## What the test does NOT assert

- **Does not assert the count is 4 anymore.** The new paradigm
  writes 3 records at ingest (raw_input + audit_event +
  pending_event). The 4th write is the Compiler's. Don't
  artificially preserve the old count.
- **Does not assert no future external services.** The test is
  about **today's** external capabilities. If a 4th external
  capability is added (e.g., a search index), add a 4th panicking
  fake and a 4th assertion.
- **Does not assert no logging.** Logging is internal, not
  external.
- **Does not assert the response is a specific format.** The
  principle is the contract; the response shape is owned by
  `IngestTextResult`.

---

## The dependency injection refactor

The current `IngestTextService` constructor
(`internal/app/ingest_text.go:63-68`) takes:
- `uow IngestionUnitOfWork`
- `ids IDGenerator`
- `now Clock`
- `logger *slog.Logger`

It does **not** take LLM, NATS, or embedder. That's actually a
good thing — the principle is preserved by the type system today
(the service literally cannot call those things because it doesn't
have references to them).

The test rename doesn't require a refactor. The panicking fakes
become useful when the **dependencies do exist** in the
constructor (e.g., after change #8 wires the outbox publisher and
NATS client into the service). The test should be **forward-
compatible**: it works today, and it works after change #8.

**Recommendation**: in the change #5 refactor, introduce the
dependency injection struct now, even if the fields are nil
today. The constructor signature is `NewIngestTextServiceWithDeps(deps IngestTextDeps)`. Today, the LLM/NATS/Embedder fields are
nil. After change #8, they're real. The test passes in both cases.

---

## What goes in change #5

1. **Rename the test** in `internal/app/ingest_text_test.go:130`:
   - From: `TestIngestDoesNotRequireDeferredExternalCapabilities`
   - To: `TestIngestHasNoDeferredExternalDependencies`
2. **Refactor the test body** to the shape above (panicking
   fakes, 3-write count, latency bound, goleak).
3. **Add the three focused tests** for LLM, NATS, embedder.
4. **Add the `IngestTextDeps` struct** to the constructor
   (forward-compatible for change #8).
5. **Update the integration test** in
   `internal/postgres/ingestion_integration_test.go` to assert the
   3-write count, not 4.
6. **Update the spec** `knowledge-core-ingestion/spec.md` to
   document the new test name and the new assertion shape (the
   spec is the "what is the inbox contract" reference; the test
   is the executable form of that spec).

---

## Spec anchors

- `proposal.md:221-226` — 4-write contract reframe
- `proposal.md:365,378` — `internal/app/ingest_text.go` mention
- `design.md:875-883` — original open question on test body
- `tasks.md:52,57` — change #5 description
- `internal/app/ingest_text.go:147-158` — current 4-write implementation
- `internal/app/ingest_text_test.go:130-154` — current test
- `research/01-3-transactional-outbox.md` — companion decision
  (the outbox INSERT is the 3rd write)
