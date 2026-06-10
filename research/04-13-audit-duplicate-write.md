# 04-13 — Audit Duplicate Write (quick win #13)

Closes quick-win #13 of the backlog. Captured 2026-06-10.

---

## TL;DR

`IngestTextService` detects duplicates (`ingest_text.go:82-86`) but
**does not write an audit event** for them. The constant
`AuditActionKnowledgeDuplicateDetected = "knowledge.duplicate_detected"`
exists in `domain/knowledge.go:109` but is never emitted. The audit
trail silently fails on duplicates — `ROADMAP.md:233` (H2) flagged
this as a HIGH finding.

**The fix is small** (~20 lines in `IngestTextService.Ingest`):

1. Inside the existing transaction, on the duplicate path, create
   an `audit_event` with action `AuditActionKnowledgeDuplicateDetected`.
2. Target: the **existing** knowledge_object (the one that was a
   duplicate, not a new one).
3. Payload: `identity_key`, `content_checksum`, `existing_object_id`,
   and the caller context.
4. No migration. The `audit_events` table already exists.
5. One new test: `TestIngestWritesAuditEventOnDuplicate`.
6. Update `TestIngestReturnsDuplicateWithoutCreatingRecords` to
   assert 1 write (the audit), not 0.

---

## The exact gap (cite + line)

### Constant exists, is never used

`internal/domain/knowledge.go:107-112`:

```go
const (
    AuditActionKnowledgeIngested         = "knowledge.ingested"
    AuditActionKnowledgeDuplicateDetected = "knowledge.duplicate_detected"
    AuditActionKnowledgeStatusChanged    = "knowledge.status_changed"
    AuditActionRelationCreated           = "relation.created"
)
```

The constant is defined. **Zero call sites** for
`AuditActionKnowledgeDuplicateDetected` in the codebase today.

### The duplicate path that should write the event

`internal/app/ingest_text.go:82-86`:

```go
existing, err := repos.Sources().FindIngestionResultByIdentityKey(
    ctx, prepared.workspaceID, prepared.identityKey)
if err == nil {
    existing.Duplicate = true
    result = existing
    return nil  // ← returns without writing audit
}
```

When a duplicate is found, the function **returns nil from the
transaction callback** with 0 writes. No audit trail.

### Where it's flagged

- `ROADMAP.md:233` (H2) — "Duplicates no dejan audit trail"
- `design.md:695-701` — risk section
- `proposal.md:450-454` — change #13 description
- `exploration.md:551, 598` — exploration findings
- `tasks.md:108, 195` — change #13 task

---

## Sub-decisions inside the change

### Target: existing object, not the call

The audit event should reference the **existing** knowledge_object
(the one that's a duplicate), not some new object. The semantic
meaning of the event is:

> "At time T, workspace W tried to ingest content C, which was
> a duplicate of object O."

So:
- `target_type = "knowledge_object"`
- `target_id = existing.ObjectID` (the existing one)
- `actor_id = req.Object.CreatedBy` (who tried to ingest)
- `payload = { identity_key, content_checksum, existing_object_id }`

### Inside the transaction

The audit write should be **inside the existing transaction**,
not after. Reasons:
- Atomic with the duplicate detection. If the audit write fails,
  the whole transaction fails (caller gets an error).
- Consistent with the rest of the code (other audit events are
  inside the tx).
- The `repos.AuditEvents()` port is already available in the
  unit of work.

### Payload contents

| Field | Value | Why |
|---|---|---|
| `identity_key` | `prepared.identityKey` | What was matched |
| `content_checksum` | `prepared.contentChecksum` | What was the input |
| `existing_object_id` | `existing.ObjectID.String()` | Which object it duplicates |

`workspace_id` and `actor_id` are already columns on `audit_events`
— no need to duplicate in the payload.

### Idempotency of the audit event

If the same caller retries the duplicate 100 times, we write 100
audit events. Is that OK?

**Yes.** Duplicates are exceptional (the client shouldn't be
hitting the API with the same content repeatedly). Each one is
interesting audit info. The volume should be low.

If volume becomes a problem in the future, add a per-`(identity_key,
time_bucket)` dedup, but YAGNI for now.

### Test: update existing, add new

Two test changes:

1. **Update** `TestIngestReturnsDuplicateWithoutCreatingRecords`
   (`ingest_text_test.go:93-128`):
   - Currently asserts `writeCount == 0`.
   - Change to: `assert 1 audit write of action
     AuditActionKnowledgeDuplicateDetected`.

2. **Add** `TestIngestWritesAuditEventOnDuplicate`:
   - Ingest content with identity key K1 → success, 4 writes
     (source, object, link, audit-ingested).
   - Ingest same content with K1 again → duplicate, 1 write
     (audit-duplicate_detected).
   - Assert the audit row's `action`,
     `target_id == existing.ObjectID`, payload contains
     `identity_key` and `existing_object_id`.

### Connection to change #5 (inbox pattern)

After change #5, the "duplicate" detection moves to the inbox
level: the `raw_inputs` table has a UNIQUE constraint on
`identity_key`, so duplicates are caught by the DB at INSERT
time. The audit event should fire on that path too.

The cleanest handoff: keep the audit event in `IngestTextService`
(now operating on the inbox), but the existing object reference
becomes the existing `raw_input` or the existing `knowledge_object`
it ultimately became.

For quick win #13, **stay in the current code path**. Change #5
will refactor anyway; the audit event moves with the refactor.

---

## What goes in change #13

1. **Edit `internal/app/ingest_text.go:82-86`** to write the
   audit event in the duplicate path.
2. **Edit `internal/app/ingest_text_test.go:93-128`** to update
   the write count assertion.
3. **Add `TestIngestWritesAuditEventOnDuplicate`** with the
   assertions above.
4. **Update `ROADMAP.md:233`** to mark H2 as ✅ Resuelto.
5. **No migration.** `audit_events` already exists.
6. **No spec change.** The audit promise is implicit in the
   paradigm; this just makes it hold.
7. **Update the backlog** to mark #13 done.

### Estimated diff

- `ingest_text.go`: +20 lines (audit event creation + return)
- `ingest_text_test.go`: -2 +25 lines (one assertion update +
  one new test)
- `ROADMAP.md`: 1 line changed

Total: ~45 lines diff. Half a day, even conservative.

---

## What this is NOT

- **Not a refactor** of the audit infrastructure. The
  `audit_events` table, the port, the constants all stay.
- **Not a behavior change** for non-duplicate ingests. The happy
  path is untouched.
- **Not a fix for H3** (the deterministic duplicate query, already
  resolved) or H4-H6 (other HIGH findings). Just H2.
- **Not a new audit action**. The action constant already exists.

---

## Risks and edge cases

### Race condition

The duplicate check + audit write are inside a transaction. If
two requests with the same identity_key arrive simultaneously,
one wins (writes the 4 records) and the other sees a duplicate
(writes the audit). Both are correct.

But: what if the "winner" transaction rolls back after the "loser"
has already written its audit? The audit row would reference an
object that doesn't exist anymore.

**Mitigation**: this race is theoretical at our scale (low
ingest volume, no transactions roll back for transient reasons).
The audit row would be a stale reference but it's an audit log
(append-only) — operators can clean it up. Document the
theoretical case in the design doc.

If the race becomes a real problem, the fix is to write the audit
AFTER the data writes, in a separate transaction. But that breaks
the atomic guarantee. For MVP, accept the theoretical race.

### Payload size

The payload is small (~3 string fields). No bloat. The
`audit_events` table doesn't have a payload size limit anyway.

### Test fixtures

The existing test `TestIngestReturnsDuplicateWithoutCreatingRecords`
uses a fake `sourceRepo` that returns a "found" `IngestTextResult`
without going through a real DB. The new test can do the same.

The `fakeSourceRepo` lives in `ingest_text_test.go:322`. Its
`FindIngestionResultByIdentityKey` is a stub. Update it to return
a real `IngestTextResult` with `ObjectID` set, so the audit event
can reference it.

### Migration of existing duplicate ingests

There may be rows in `audit_events` from before this change that
represent the FIRST ingest of a content. The duplicate ingests
that happened before this change have NO audit row. That's OK
— the audit log is forward-looking. Old duplicates are just not
auditable retroactively. Document this in the commit message.

---

## What this DOES enable

- **Compliance trail** for ingests. Auditors can answer "did this
  content get ingested multiple times?" by counting
  `knowledge.duplicate_detected` events.
- **Anomaly detection**. A burst of duplicate events from a
  caller is a signal of a misbehaving client or a retry loop.
- **Resolves H2 from ROADMAP.md**. The HIGH finding can move to
  ✅ Resuelto. One less open risk.

---

## Spec anchors

- `internal/domain/knowledge.go:107-112` — the unused constant
- `internal/app/ingest_text.go:82-86` — the duplicate path
- `internal/app/ingest_text_test.go:93-128` — the test to update
- `ROADMAP.md:233` — H2 finding
- `design.md:695-701` — risk section
- `proposal.md:450-454` — change #13 description
- `exploration.md:551, 598` — exploration findings
- `tasks.md:108, 195` — change #13 task
