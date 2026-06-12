# 01-3 — Transactional Outbox for NATS Publish (change #8 `event-bus-nats`)

Captured from working session 2026-06-10. Addresses backlog §1.3.

---

## TL;DR

The write-then-publish gap is **real and dangerous**: today, the
acquisition layer writes `raw_inputs` + `audit_events` in one DB
transaction and then publishes `raw_input.received` to NATS in a
separate step. If the publish fails (NATS down, network blip,
process crash, OS kill), the event is lost. The `raw_input` row
sits in `status = 'pending'` forever. Workers never see it.

**Adopt the transactional outbox pattern with these decisions**:

1. **Table**: `pending_events` (the backlog's name) — schema
   below. Note: `outbox` is the more common name in the
   industry; either works.
2. **Insert site**: in the **same DB transaction** as the
   `raw_inputs` INSERT (and every other `audit_events` INSERT the
   acquisition does). Same for the Compiler: outbox INSERT is in
   the same transaction as the `knowledge_objects`,
   `lifecycle_events`, `object_versions`, `relations` writes.
3. **Drain mechanism**: **polling with `FOR UPDATE SKIP LOCKED`**.
   Every 100 ms. Simple, observable, easy to scale horizontally.
4. **Mark, don't delete**: `published_at TIMESTAMPTZ` column. Keep
   the row for observability. GC after 7 days via a separate job.
5. **Drainer concurrency**: 1 process for MVP. Multiple processes
   are safe (`SKIP LOCKED` prevents double-publish); horizontal
   scale is a config change, not a code change.
6. **Backpressure**: alert on `pending_count > 10_000`. The system
   is unhealthy long before that. Acquisition is **not blocked** by
   outbox depth (the INSERT is O(1) inside the transaction).
7. **Connection to §1.2**: the drainer is at-least-once. NATS
   consumers dedupe on `event_id`. The idempotency story from
   §1.2 carries over unchanged.

---

## The problem (write-then-publish gap)

The acquisition flow today (`design.md:104-119`):

```
BEGIN TRANSACTION
  INSERT INTO raw_inputs (...)
  INSERT INTO audit_events (...)
COMMIT
publish("raw_input.received", payload)   ← outside the transaction
```

Two failure modes:

- **DB commits, publish fails**: `raw_input` exists in `pending`,
  no worker is notified. Stuck.
- **DB commits, process crashes before publish**: same outcome.

This is the **dual-write problem**, well-documented in the
microservices literature (Chris Richardson, microservices.io). The
fix is the **transactional outbox** pattern.

### What the proposal already says

From `design.md:135-136`:

> "Subject missing: publish fails; caller rolls back its own
> write (acquisition layer relies on the outbox pattern — see §11
> Open Questions)."

The design document **already gestures at the outbox** but defers
the decision to a chained change. This is that change.

---

## Options analysis

### Option A — Transactional outbox (recommended)

```
BEGIN TRANSACTION
  INSERT INTO raw_inputs (...)
  INSERT INTO audit_events (...)
  INSERT INTO pending_events (subject, payload, event_id)
COMMIT
```

A separate **drainer process** polls `pending_events` for rows
where `published_at IS NULL`, publishes each one to NATS, then
updates `published_at = now()`.

**Pros**:
- Strong reliability: the event will be published eventually, or
  an operator gets paged.
- Strict ordering per `event_id` (FIFO with `ORDER BY id`).
- Observability: the outbox table is the source of truth for
  "what should have been published by now".
- Decouples the hot path (acquisition is O(1) extra) from the
  publish path (drainer can lag without blocking writers).

**Cons**:
- Extra table, extra drainer process, extra GC job.
- Slightly higher publish latency (drain interval + publish time,
  e.g. 100 ms + 5 ms = 105 ms p50).
- Drainer must be HA'd or it becomes a SPOF.

### Option B — Inline retry on publish failure

```
BEGIN TRANSACTION
  INSERT INTO raw_inputs (...)
  INSERT INTO audit_events (...)
COMMIT

for attempt in 1..N:
  try publish("raw_input.received", payload)
  if success: return ok
  sleep(backoff(attempt))

return 5xx  ← caller retries the whole request
```

**Pros**:
- Lower latency on the happy path.
- No extra infrastructure.
- `identity_key` UNIQUE constraint on `raw_inputs` makes caller
  retries safe (no duplicate row).

**Cons**:
- If all N retries fail, the event is **lost**. `raw_input` is in
  `pending` forever. **No recovery path.**
- Caller is coupled to publish latency. If NATS is slow, HTTP
  responses are slow.
- Operational signal of "events lost" only comes from a stale
  `pending` row alert, by which time it's too late.

### Option C — Publish first, then DB

```
publish("raw_input.received", payload)  ← outside the transaction
BEGIN TRANSACTION
  INSERT INTO raw_inputs (...)
  INSERT INTO audit_events (...)
COMMIT
```

**Rejected**: if the DB transaction fails, the worker dequeues a
phantom event for a non-existent row. Worse than Option B.

### Option D — Two-phase commit (DB + NATS)

NATS does not support 2PC. Postgres does, but coordinating a
distributed transaction with NATS is not a thing. **Rejected**.

### Option E — NATS as the source of truth

Worker writes to DB on consumption. The hot path is "publish to
NATS, return OK to caller". DB writes are async.

**Rejected**: caller needs an `ObjectID` back synchronously
(`design.md:419-420`, the `Write(ctx, in) (ObjectID, error)`
contract). We can't return an ID if the DB write hasn't happened.

### Option F — CDC (Debezium / logical replication)

Postgres logical replication streams DB changes to a connector
(Debezium), which publishes to NATS. No application-level outbox
table.

**Pros**: no extra INSERT in the hot path; the trigger is in the
DB; rich ecosystem.

**Cons**: operational complexity (Debezium cluster, schema
registry, offset management, exactly-once semantics, monitoring).
Overkill for our scale.

**Status**: rejected for MVP. Re-evaluate at > 10k events/sec
sustained.

### Summary

| Option | Reliability | Latency | Complexity | Verdict |
|---|---|---|---|---|
| **A. Outbox** | **Strong** | +100ms p50 | Medium | **Recommended** |
| B. Inline retry | Weak (lossy) | Lower | Low | Acceptable as MVP shortcut, not for production |
| C. Publish first | Very weak (phantoms) | Lower | Low | Rejected |
| D. 2PC | Strong | High | Very high | Not possible with NATS |
| E. NATS as truth | Strong | Lower | Medium | Rejected (sync ID required) |
| F. CDC / Debezium | Strong | Lower | High | Rejected for MVP |

---

## Sub-decisions within the outbox

### Schema

```sql
CREATE TABLE pending_events (
    id              BIGSERIAL PRIMARY KEY,
    event_id        UUID        NOT NULL UNIQUE,        -- idempotency key for NATS
    subject         TEXT        NOT NULL,                -- e.g. "raw_input.received"
    payload         JSONB       NOT NULL,                -- event body
    headers         JSONB       NOT NULL DEFAULT '{}',  -- optional NATS headers
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,                         -- NULL = pending
    publish_attempts INT        NOT NULL DEFAULT 0,
    last_error      TEXT,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now()  -- for backoff
);

-- Hot index for the drainer: only the pending rows, in insertion order.
CREATE INDEX pending_events_pending_idx
    ON pending_events (next_attempt_at, id)
    WHERE published_at IS NULL;
```

Notes:
- `event_id` is the idempotency key on the NATS side. The drainer
  sets the `Nats-Msg-Id` header to `event_id` (NATS will dedupe on
  it if the consumer enables dedup). The unique constraint is
  belt-and-suspenders.
- `next_attempt_at` lets the drainer implement backoff without
  blocking the entire table: failed rows get pushed into the
  future.
- `headers` is for things like `Workspace-ID` that the consumer
  may want without parsing the body.

### Drain mechanism: polling vs NOTIFY vs CDC

| Mechanism | Latency | Complexity | Verdict |
|---|---|---|---|
| **Polling** (`SELECT ... FOR UPDATE SKIP LOCKED`) | ~100ms p50 | Low | **Recommended for MVP** |
| Postgres `NOTIFY` / `LISTEN` | <10ms p50 | Medium | Nice for v2 |
| CDC / Debezium | <50ms p50 | High | Rejected for MVP |

**Polling implementation**:

```sql
-- Drainer query (run every 100ms)
SELECT id, event_id, subject, payload, headers
FROM pending_events
WHERE published_at IS NULL
  AND next_attempt_at <= now()
ORDER BY id
LIMIT 100
FOR UPDATE SKIP LOCKED;
```

The drainer:
1. Runs the query above in a transaction.
2. For each row, attempts `js.PublishAsync(subject, payload, headers)`.
3. On success: `UPDATE pending_events SET published_at = now() WHERE id = $1`.
4. On failure: `UPDATE pending_events SET publish_attempts = publish_attempts + 1, last_error = $1, next_attempt_at = now() + backoff(attempt) WHERE id = $2`.
5. Commits the transaction.

### Mark vs delete

**Mark, don't delete**. Reasons:
- Observability: the outbox IS the audit log of "what was
  published". A delete removes that history.
- Debugging: when a consumer says "I never got event X", you
  query `pending_events` to see if it was published and when.
- Compliance: some events have audit-trail requirements.

GC policy: a separate daily job deletes rows where
`published_at < now() - INTERVAL '7 days'`. The 7-day window is
tunable.

### Polling interval

- **100ms p50 latency** is the default. Acquisition writes
  `pending_events` in the same transaction; the drainer catches it
  on the next tick.
- For 1k events/sec sustained, this is 100 events per tick. The
  drainer can publish faster than the tick rate, so the queue
  stays near-empty.
- For 10k events/sec sustained, reduce the interval to 25ms.
- The interval is a tunable, not a code change.

### Drainer concurrency

- **1 process is enough for MVP**. Postgres handles the
  concurrency primitive (`FOR UPDATE SKIP LOCKED`).
- **Multiple processes are safe** (just point them at the same
  `pending_events` table). `SKIP LOCKED` ensures no two drainers
  pick up the same row.
- Run as a separate deployment unit, not a goroutine in the
  API process. (See backlog risk §2.2: "subscriptions must not
  block shutdown" applies to drainers too.)

### Failure modes

| Failure | Behavior | Recovery |
|---|---|---|
| NATS down | Drainer keeps trying. Rows accumulate in `pending_events`. | NATS comes back, drainer catches up. |
| DB connection lost mid-publish | Same as above. Next iteration republishes. Consumer dedupes. | Auto. |
| Drainer process crash mid-batch | Same as above. Consumer dedupes on `event_id`. | Auto. |
| Event payload malformed (shouldn't happen — it's our own INSERT) | Drainer logs, increments `publish_attempts`, sets `last_error`. | Manual fix or quarantine. |
| Drainer misconfigured (wrong subject) | All events go to a non-existent subject. NATS rejects. Drainer logs. | Manual fix. |
| `pending_events` table grows unbounded | Indicates NATS is down or drainer is broken. | Alert. |

### Backpressure

- Alert on `SELECT count(*) FROM pending_events WHERE published_at IS NULL > 10000`.
- Alert on `pending_count` growth rate (e.g. delta > 1000/sec
  for 60s).
- Acquisition is **not** blocked by outbox depth — the INSERT is
  O(1) inside the transaction. The DB does the work atomically.

### What events go in the outbox

All NATS publishes from the four subjects in
`knowledge-pipeline/spec.md:149-158`:

- `raw_input.received` — published by the acquisition layer
- `lifecycle.transitioned` — published by the Compiler
- `relation.created` — published by the Compiler or
  RelationBuilder
- `object.canonicalized` — published by the Compiler

The acquisition layer has 1 INSERT site. The Compiler has 3 INSERT
sites (one per event type, in the same transaction as the data
writes).

### What events do NOT go in the outbox

- Anything that's not a NATS publish. (Audit events go to
  `audit_events` directly, which the outbox mirrors for the
  publish side.)
- Future events that bypass the DB transaction (none today).

---

## Connection to §1.2 (NACK semantics)

The two decisions are tightly coupled. The `pending_events` table
is the producer-side of the same at-least-once contract that
§1.2 specifies for the consumer-side:

| §1.3 (producer) | §1.2 (consumer) |
|---|---|
| Outbox `INSERT` is part of the data transaction | NATS consumer config: `BackOff`, `MaxDeliver` |
| Drainer is at-least-once (publishes might repeat) | Workers are idempotent on `raw_input_id` |
| `event_id` is the idempotency key on the bus | `Nats-Msg-Id` header for dedup (if enabled) |
| `MaxAckPending` bounds consumer concurrency | Drainer `LIMIT 100` bounds producer concurrency |

**One property guarantees the other**: as long as consumers dedupe
on `event_id` (or `raw_input_id`), the producer can be at-least-
once without losing exactly-once semantics at the system level.

---

## What goes in change #8

The change spec should commit to:

1. **Migration** `0016_pending_events.sql` with the schema above.
2. **Outbox writer** (Go package): a `Record(ctx, tx, subject, payload, headers)`
   function that the acquisition and Compiler call inside their
   transactions. Takes `tx` as a parameter — it does NOT open its
   own.
3. **Drainer** (Go service or goroutine): the polling loop
   described above. Runs as a separate process.
4. **Refactor** the acquisition layer to call `Record` instead of
   the current inline `publish` call.
5. **Refactor** the Compiler to call `Record` for
   `lifecycle.transitioned`, `relation.created`,
   `object.canonicalized` events.
6. **Test plan**:
   - Acquisition writes → row in `pending_events` in the same tx.
   - Drainer picks up the row, publishes, marks.
   - NATS down → rows accumulate, drainer catches up when NATS is back.
   - Drainer crash mid-batch → consumer dedupes.
   - Manual `UPDATE published_at = NULL` on a row → drainer
     republishes (idempotency check on consumer side).
7. **GC job**: daily delete of rows where `published_at < now() - 7 days`.
8. **Metrics**: `pending_events_depth`, `pending_events_oldest_age`,
   `pending_events_publish_rate`, `pending_events_error_rate`.
9. **Alerts**: `pending_events_depth > 10_000`,
   `pending_events_oldest_age > 60s`.

---

## Risks and edge cases

- **Drainer as SPOF**: mitigated by running multiple drainers
  (`SKIP LOCKED` makes it safe).
- **Outbox table growth**: mitigated by GC job + backpressure
  alerts.
- **Ordering**: events are FIFO per `event_id`, but across
  `event_id`s there's no ordering. The spec already says
  per-subject FIFO with a single stream consumer (`design.md:129-
  130`). This holds.
- **Payload size**: NATS has a message size limit (1MB default).
  If payloads exceed this, we need a "claim check" pattern (store
  payload in S3/equivalent, publish a reference). Out of scope for
  MVP — the events we publish are pointers (`raw_input_id`,
  `object_id`), not bulk data.
- **NATS ack-vs-drain race**: a drainer that marks a row as
  published AFTER NATS has acked the publish is safe — the next
  iteration will republish, consumer dedupes. A drainer that marks
  BEFORE NATS has acked is unsafe — if the drainer crashes between
  mark and ack, the event is lost. The implementation must mark
  AFTER the publish ack.

---

## What this is NOT

- **Not a replacement for the audit log.** `audit_events` is the
  audit log. `pending_events` is the outbox for the bus. They
  coexist.
- **Not a queue manager.** NATS is the queue manager. The outbox
  is the bridge between the DB transaction and NATS.
- **Not a CDC tool.** The events in the outbox are application-
  level events, not DB change events. Debezium-style CDC is a
  different pattern.

---

## Spec anchors

- `design.md:104-119` — acquisition layer failure modes
- `design.md:120-136` — §2.2 NATS Event Bus
- `design.md:135-136` — outbox mentioned in passing, deferred
- `design.md:854-873` — §11 Open Questions (NACK + outbox)
- `proposal.md:259-282` — processing pipeline + subjects
- `tasks.md:73-79` — change #8 description
- `specs/knowledge-pipeline/spec.md:138-204` — 7-worker contract
- `research/01-2-nats-nack-semantics.md` — companion decision
