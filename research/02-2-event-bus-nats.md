# 02-2 — Event Bus NATS (change #8)

Closes HIGH risk §2.2 of the backlog. Captured 2026-06-10.

---

## TL;DR

Change #8 lands the event bus substrate: NATS JetStream for
durable publish/subscribe, the `pending_events` outbox table
from §1.3, the drainer process, the DLQ-watcher from §1.2, the
`EventPublisher` port, and the **shutdown coordination** that
ensures NATS drains before pgx closes (closes backlog risk
"subscriptions must not block shutdown").

Subscribers (the 7 workers) come in a later change. This change
is the publish side + substrate only.

**Twelve concrete decisions**:

1. **NATS JetStream** (not core NATS) for the 4 event streams.
   File storage, `Limits` retention, 7-day `MaxAge`.
2. **`EventPublisher` port** in `internal/app` (interface).
3. **NATS adapter** in `internal/natsbus` (new package).
4. **`outbox` writer** (`Record(ctx, tx, subject, payload, headers)`)
   as a port + Postgres impl. **Takes `tx` as parameter** —
   the INSERT runs inside the caller's transaction.
5. **Drainer** as a separate process (`cmd/drainer/main.go`),
   not a goroutine in the API.
6. **Drain loop**: 100 ms tick, `FOR UPDATE SKIP LOCKED`, LIMIT
   100, mark after NATS ack.
7. **DLQ-watcher** as a separate process (`cmd/dlq-watcher/...`),
   subscribes to `MAX_DELIVERIES.<stream>.<consumer>` advisories
   and republishes to `{subject}.dlq`.
8. **Refactor `IngestTextService`** to call `outbox.Record` for
   `raw_input.received` instead of a direct publish.
9. **Refactor Compiler (3 sites)** to call `outbox.Record` for
   `lifecycle.transitioned`, `relation.created`,
   `object.canonicalized`.
10. **Shutdown order**: stop HTTP → drain NATS subscriptions →
    drain outbox (final flush) → close NATS → close pgx.
11. **Health check** at `/v1/health` reports NATS + DB + drainer
    status (the drainer is a separate process; the API doesn't
    know its state directly — a heartbeat table or `last_drain_at`
    metric).
12. **Metrics**: `pending_events_depth`, drainer publish rate,
    NATS advisory count, DLQ depth.

---

## What change #8 needs to do

Per `tasks.md:73-78`, `proposal.md:422-446`, `design.md:120-136`:

- Add `github.com/nats-io/nats.go` to `go.mod`.
- `EventPublisher` port with a NATS adapter.
- Publish 4 subjects: `raw_input.received`,
  `lifecycle.transitioned`, `relation.created`,
  `object.canonicalized`.
- NACK semantics (closed by §1.2).
- Outbox pattern (closed by §1.3).
- Subscriptions come "in a future worker" — out of scope here.
- Risk: NATS client + pgx pool; subscriptions must not block
  shutdown.

---

## Sub-decisions inside change #8

### Decision 1: JetStream vs core NATS

| Option | Pros | Cons |
|---|---|---|
| **A. JetStream (recommended)** | Durable; replay; consumer ack; the substrate we decided in §1.2 | Operational complexity (NATS server with JetStream enabled) |
| B. Core NATS (fire-and-forget) | Lighter; no persistence | No replay, no ack, no DLQ semantics |

→ **A**. Without JetStream, the §1.2 NACK semantics (BackOff,
MaxDeliver, DLQ advisory) don't work.

### Decision 2: Stream config per subject

For each of the 4 subjects (`raw_input.received`,
`lifecycle.transitioned`, `relation.created`,
`object.canonicalized`):

```go
StreamConfig{
    Name:     "EVENTS",
    Subjects: []string{"raw_input.received", "lifecycle.transitioned", "relation.created", "object.canonicalized"},
    Storage:  FileStorage,
    Retention: Limits,
    MaxAge:   7 * 24 * time.Hour,
    MaxMsgs:  1_000_000,
    MaxBytes: 10 * 1024 * 1024 * 1024, // 10 GB
}
```

**Decision**: one stream `EVENTS` with 4 subjects (not 4 separate
streams). Reasons:
- Simpler ops (one stream to monitor).
- Subjects are versioned together (a schema change affects one
  stream).
- The per-subject consumer config gives us per-worker control
  anyway.

If a single subject grows disproportionately (e.g.,
`raw_input.received` from a high-traffic source), split that
subject into its own stream then. For MVP, one stream is fine.

### Decision 3: `EventPublisher` port

New file: `internal/app/event_publisher.go`.

```go
package app

// EventPublisher is the port for publishing domain events to the
// bus. Implementations may use NATS JetStream, an outbox, or
// a no-op for tests. The IngestTextService and the Compiler
// depend on this port, never on a concrete transport.
type EventPublisher interface {
    Publish(ctx context.Context, subject string, payload []byte, headers map[string]string) error
}
```

`Publish` is synchronous (returns when the bus has accepted the
message). The implementation is responsible for any
buffering/retries. The interface is small on purpose — no
streaming, no headers API beyond a simple map.

### Decision 4: NATS adapter

New package: `internal/natsbus`. Contains:

- `NATSClient` — wraps `nats.Conn` + `jetstream.JetStream` +
  reconnection logic.
- `JetStreamPublisher` — implements `app.EventPublisher` by
  calling `js.PublishAsync(subject, payload, nats.MsgId(headers["Nats-Msg-Id"]))`.
- `JetStreamSubscriber` — future change. Creates consumers per
  worker with the §1.2 config.

```go
type JetStreamPublisher struct {
    js jetstream.JetStream
    logger *slog.Logger
}

func (p *JetStreamPublisher) Publish(ctx context.Context, subject string, payload []byte, headers map[string]string) error {
    msgID := headers["Nats-Msg-Id"]
    ack, err := p.js.PublishAsync(subject, payload, jetstream.WithMsgID(msgID))
    if err != nil {
        return err
    }
    select {
    case <-ack.Ok():
        return nil
    case <-ack.Err():
        return ack.Err()
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

`PublishAsync` returns a `PubAckFuture`. The `select` waits for
the ack, the error, or the context cancellation. The publish is
synchronous from the caller's POV; the underlying mechanism is
async with backpressure.

### Decision 5: Outbox writer

New port + Postgres impl.

`internal/app/outbox.go`:

```go
type Outbox interface {
    Record(ctx context.Context, tx Tx, subject string, payload []byte, headers map[string]string) error
}

type Tx interface {
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
```

`Tx` is the minimal interface a writer needs. The implementation
takes a `*pgx.Tx` or a `pgxpool.Conn` — the interface keeps the
port clean.

`internal/postgres/outbox.go`:

```go
type OutboxRepo struct{}

func (r *OutboxRepo) Record(ctx context.Context, tx app.Tx, subject string, payload []byte, headers map[string]string) error {
    headersJSON, err := json.Marshal(headers)
    if err != nil {
        return err
    }
    eventID := uuid.New()
    _, err = tx.Exec(ctx, `
INSERT INTO pending_events (event_id, subject, payload, headers)
VALUES ($1, $2, $3, $4)`,
        eventID, subject, payload, headersJSON)
    return err
}
```

**Critical**: the `Record` call **takes the caller's `tx`**. The
INSERT runs inside the caller's transaction. This is the atomic
guarantee from §1.3: the data write and the outbox write are
all-or-nothing.

### Decision 6: Drainer process

New: `cmd/drainer/main.go`.

```go
func main() {
    logger := newLogger(...)
    pool, err := postgres.Open(ctx, cfg.DatabaseDSN)
    if err != nil { ... }
    defer pool.Close()
    
    nc, js, err := natsbus.Connect(cfg.NATSURL, logger)
    if err != nil { ... }
    defer nc.Drain()
    
    publisher := natsbus.NewJetStreamPublisher(js, logger)
    drainer := postgres.NewOutboxDrainer(pool, publisher, logger)
    
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            logger.Info("drainer stopping")
            return
        case <-ticker.C:
            n, err := drainer.DrainBatch(ctx, 100)
            if err != nil { logger.Error(...) }
            if n > 0 { logger.Info("drained", "count", n) }
        }
    }
}
```

`OutboxDrainer.DrainBatch`:

```go
func (d *OutboxDrainer) DrainBatch(ctx context.Context, limit int) (int, error) {
    tx, err := d.pool.Begin(ctx)
    if err != nil { return 0, err }
    defer tx.Rollback(ctx)
    
    rows, err := tx.Query(ctx, `
SELECT id, event_id, subject, payload, headers, publish_attempts
FROM pending_events
WHERE published_at IS NULL
  AND next_attempt_at <= now()
ORDER BY id
LIMIT $1
FOR UPDATE SKIP LOCKED`, limit)
    if err != nil { return 0, err }
    defer rows.Close()
    
    var drained int
    for rows.Next() {
        var id int64
        var eventID uuid.UUID
        var subject, payload string
        var headers []byte
        var attempts int
        if err := rows.Scan(&id, &eventID, &subject, &payload, &headers, &attempts); err != nil {
            return drained, err
        }
        
        var headersMap map[string]string
        json.Unmarshal(headers, &headersMap)
        if headersMap == nil { headersMap = map[string]string{} }
        headersMap["Nats-Msg-Id"] = eventID.String()
        
        err := d.publisher.Publish(ctx, subject, []byte(payload), headersMap)
        if err != nil {
            // backoff
            backoff := computeBackoff(attempts)
            tx.Exec(ctx, `
UPDATE pending_events
SET publish_attempts = publish_attempts + 1,
    last_error = $1,
    next_attempt_at = now() + $2::interval
WHERE id = $3`, err.Error(), backoff, id)
            continue
        }
        tx.Exec(ctx, `
UPDATE pending_events
SET published_at = now()
WHERE id = $1`, id)
        drained++
    }
    return drained, tx.Commit(ctx)
}
```

Notes:
- `next_attempt_at` lets the drainer back off without blocking
  other rows.
- `computeBackoff(attempts)` returns `1s, 5s, 30s, 2m, 10m` per
  §1.2.
- The drainer uses `defer tx.Rollback(ctx)` so partial failures
  don't leave transactions open.

### Decision 7: DLQ-watcher

New: `cmd/dlq-watcher/main.go`. Subscribes to all 4
`MAX_DELIVERIES.<stream>.<consumer>` advisories and republishes
to `{subject}.dlq`.

The implementation is similar to the drainer: NATS subscriber
that re-publishes to a dead-letter subject.

```go
func main() {
    nc, js, err := natsbus.Connect(...)
    defer nc.Drain()
    
    // Subscribe to advisories for all 4 subjects
    advisorySubject := "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.*"
    sub, err := js.Subscribe(advisorySubject, func(msg *nats.Msg) {
        // Extract original subject + sequence from advisory payload
        var advisory natsbus.MaxDeliveriesAdvisory
        json.Unmarshal(msg.Data, &advisory)
        
        // Get the original message from the stream
        original, err := js.GetMsg("EVENTS", advisory.StreamSeq)
        if err != nil { return }
        
        // Republish to {subject}.dlq
        dlqSubject := advisory.Subject + ".dlq"
        js.PublishAsync(dlqSubject, original.Data, jetstream.WithMsgID(original.Header.Get("Nats-Msg-Id")))
    })
    defer sub.Unsubscribe()
    
    <-ctx.Done()
}
```

The DLQ-watcher is read-only with respect to the outbox; it
just moves messages from one subject to another.

### Decision 8: Refactor IngestTextService

The current `IngestTextService.Ingest` (`ingest_text.go:70-186`)
has 4 writes inside the transaction: source, knowledge_object,
object_source, audit_event. After change #5 (the inbox), it'll
be raw_input + audit_event + pending_events. The `pending_events`
insert is what change #8 lands.

The refactor:

```go
// Before (hypothetical future IngestTextService after #5)
err = s.uow.WithinIngestionTx(ctx, func(ctx context.Context, repos IngestionRepositories) error {
    // ... existing writes ...
    if err := repos.RawInputs().Create(ctx, rawInput); err != nil { return err }
    if err := repos.AuditEvents().Create(ctx, auditEvent); err != nil { return err }
    
    // NEW: record the event in the outbox
    if err := s.outbox.Record(ctx, tx, "raw_input.received", payload, headers); err != nil { return err }
    
    return nil
})
```

**Critical**: the `Record` call uses the same `tx` as the data
writes. Atomic.

### Decision 9: Refactor Compiler (3 sites)

The Compiler (change #7) emits 3 event types:
- `lifecycle.transitioned` — every state transition
- `relation.created` — for each new relation
- `object.canonicalized` — when an object reaches `canonical`

Each emit site:

```go
// Inside the Compiler's transaction
if err := s.outbox.Record(ctx, tx, "lifecycle.transitioned", payload, headers); err != nil {
    return err
}
```

The Compiler is in change #7 (knowledge-compiler), not #8. But
the `outbox.Record` API is delivered in #8, so change #7 uses
it from day 1.

### Decision 10: Shutdown order

This is the **specific risk the backlog flags** ("subscriptions
must not block shutdown"). The order in `cmd/api/main.go:148-172`
today is:

1. Wait for shutdown signal
2. `server.Shutdown(shutdownCtx)` — drains in-flight HTTP
3. Wait for Telegram bot
4. `dbCloser()` — close pgx pool

With NATS + drainer, the order becomes (for `cmd/api/main.go`):

1. Wait for shutdown signal
2. `server.Shutdown(shutdownCtx)` — drain HTTP
3. Wait for Telegram bot
4. (NEW) If the API also runs the drainer: drain NATS
   subscriptions
5. (NEW) If the API also runs the DLQ-watcher: drain NATS
   subscriptions
6. (NEW) Final outbox flush (drain remaining `pending_events`)
7. `nc.Drain()` — close NATS connection
8. `dbCloser()` — close pgx pool

The drainer is a **separate process** (`cmd/drainer/main.go`),
so it has its own shutdown. Its order is:

1. Wait for shutdown signal
2. Stop accepting new batches
3. Drain current batch (wait for in-flight publishes to ack)
4. `nc.Drain()` — close NATS
5. `pool.Close()` — close pgx

The DLQ-watcher follows the same pattern.

**Critical rule**: NATS must drain BEFORE pgx closes. If pgx
closes first, in-flight drainer transactions fail mid-ack and
messages get republished → consumer dedupes (safe) but it's
wasteful.

### Decision 11: Health check

`/v1/health` should report NATS + DB status. But the drainer
is a separate process. The API doesn't directly know the
drainer's state.

Two options:
- **Heartbeat table**: the drainer writes `last_heartbeat_at`
  to a small table every tick. The API reads it. If stale
  (>10s), drainer is unhealthy.
- **Metric endpoint**: the drainer exposes `/metrics` (Prometheus);
  the API doesn't read it directly but a sidecar does.

**Decision**: heartbeat table. Simpler. The drainer writes a
single row per tick. The API reads it on `/v1/health`.

```sql
CREATE TABLE drainer_heartbeat (
    drainer_id TEXT PRIMARY KEY,
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    last_drain_count INT NOT NULL DEFAULT 0,
    last_error TEXT
);
```

The drainer does `INSERT ... ON CONFLICT (drainer_id) DO UPDATE`
every tick. Cheap.

### Decision 12: Metrics

Per §1.2 + §1.3, the metrics that matter:

| Metric | Type | Source |
|---|---|---|
| `pending_events_depth` | gauge | API reads `COUNT(*) FROM pending_events WHERE published_at IS NULL` |
| `pending_events_oldest_age` | gauge | API reads `MIN(now() - created_at) WHERE published_at IS NULL` |
| `pending_events_publish_rate` | counter | drainer increments on success |
| `pending_events_error_rate` | counter | drainer increments on failure |
| `nats_advisory_total` | counter | DLQ-watcher increments per advisory |
| `dlq_depth_per_subject` | gauge | API reads `COUNT(*) FROM NATS stream per subject.dlq` |
| `drainer_lag_seconds` | gauge | drainer measures `now() - event.created_at` after publish |

These are scraped by Prometheus (or a sidecar reads them). The
API exposes `/metrics` in standard format.

---

## What goes in change #8

1. **`go.mod`**: add `github.com/nats-io/nats.go`.
2. **New migration `migrations/0016_pending_events.sql`**: the
   `pending_events` table + indexes (per §1.3 schema).
3. **New migration `migrations/0017_drainer_heartbeat.sql`**:
   the `drainer_heartbeat` table.
4. **New port `internal/app/event_publisher.go`**: the
   `EventPublisher` interface.
5. **New port `internal/app/outbox.go`**: the `Outbox` interface
   + `Tx` interface.
6. **New package `internal/natsbus`**: the NATS client wrapper
   + `JetStreamPublisher` impl.
7. **New `internal/postgres/outbox.go`**: the `OutboxRepo`
   Postgres impl.
8. **New `internal/postgres/outbox_drainer.go`**: the
   `OutboxDrainer` with `DrainBatch` method.
9. **New `cmd/drainer/main.go`**: the drainer process.
10. **New `cmd/dlq-watcher/main.go`**: the DLQ-watcher process.
11. **New `internal/natsbus/consumer_config.go`**: shared
    constants for the consumer config (per §1.2).
12. **Refactor `cmd/api/main.go`**: wire the EventPublisher
    (via outbox), update shutdown order.
13. **Refactor `internal/app/ingest_text.go`**: replace any
    direct publish (none today; just the future path) with
    `outbox.Record`.
14. **Config additions** in `internal/config/config.go`:
    `NATS_URL`, `NATS_STREAM_NAME`, `DRAINER_TICK_MS`,
    `OUTBOX_BATCH_SIZE`, etc.
15. **Tests**:
    - `internal/postgres/outbox_test.go` — Record + DrainBatch
    - `internal/natsbus/publisher_test.go` — Publish + ack flow
    - `cmd/drainer/main_test.go` — drainer lifecycle
    - `cmd/dlq-watcher/main_test.go` — DLQ routing
    - Integration test: ingest → outbox record → drain → NATS
      publish → DLQ advisory → DLQ subject has the message
16. **Shutdown integration test**: send SIGTERM, verify NATS
    drains before pgx closes.
17. **Update `design.md`** to remove the "subscriptions must not
    block shutdown" risk (now resolved).
18. **Update `tasks.md`** to mark #8 done.

---

## What this is NOT

- **Not the subscribers (the 7 workers)**. Those come in a
  later change (likely the knowledge-compiler change #7 or a
  dedicated `event-subscribers` change).
- **Not a NATS cluster setup**. Single-node NATS is fine for
  MVP. Multi-node cluster is a future ops change.
- **Not a debezium / CDC approach**. We chose the outbox over
  CDC (per §1.3).
- **Not a halfvec / HNSW / etc.** Out of scope.
- **Not a refactor of the existing `IngestTextService` 4-write
  contract**. The change #5 inbox refactor owns that.

---

## Risks and edge cases

### Risk 1: NATS client + pgx connection pool

The drainer needs pgx access. The API also needs pgx. They can
**share the same pool** (different `*sql.DB` / `pgxpool.Pool`
connections are not exclusive) or have separate pools.

**Decision**: separate pools. The drainer's pool can be tuned
independently (`MaxConns = 10` vs API's `MaxConns = 50`). The
drainer doesn't compete with the API for connections.

### Risk 2: Outbox table growth

If NATS is down for 1 hour and the API is ingesting at 10/sec,
the outbox grows by 36,000 rows. That's fine. If NATS is down
for 1 day at 10/sec, 864,000 rows. Still fine.

The issue is at high write rates. At 10k events/sec, even
minutes of NATS downtime are problematic. Mitigations:
- Monitor `pending_events_depth` and alert.
- Backpressure: if depth > 100k, the API can slow down ingest.
  (YAGNI for MVP, but document the path.)

### Risk 3: Drainer crash mid-batch

The drainer selects rows `FOR UPDATE SKIP LOCKED`, publishes,
and updates. If it crashes:
- Locked rows are released when the transaction rolls back.
- The drainer restarts, picks up the same rows next tick.
- Consumers dedupe on `event_id` (NATS MsgId).

Safe. Tested in §1.3.

### Risk 4: Clock skew between drainer and NATS server

`PublishAsync` is async. The drainer doesn't care about the
clock — NATS is the source of truth for ordering and ack
timing. No clock skew concern.

### Risk 5: Drainer competes with itself

If two drainer processes run, `FOR UPDATE SKIP LOCKED` ensures
they don't double-publish. Tested.

### Risk 6: `nats.go` library version

`github.com/nats-io/nats.go` v2.x is the current line. v2
introduced the new `jetstream` package. v1 used
`nats.JetStreamContext` (legacy). Use v2.6+ (latest stable as
of this writing).

### Risk 7: Backpressure on `PublishAsync`

`PublishAsync` returns a `PubAckFuture`. The drainer waits for
the ack with a context timeout. If the queue is full, the
publish blocks (backpressure). This is **good** — it means the
drainer won't get ahead of NATS. But it means the drainer
ticks can take longer than 100ms. That's fine.

### Risk 8: Outbox INSERT inside an aborted transaction

The outbox writer takes a `Tx`. If the caller's transaction
aborts, the outbox INSERT is also aborted. **No leaks.** The
event is never published, and the data is never persisted.

This is the **whole point** of the pattern. Verified in §1.3.

### Risk 9: Multi-subject event ordering

Per `design.md:129-130`, ordering is per-subject FIFO. Across
subjects, no ordering. The 4 subjects emit independently. If
two events for the same `object_id` need to be processed in
order, the consumer-side (workers) handles that via the
`raw_input_id` key.

### Risk 10: NATS server failure during migration run

The migration is `CREATE TABLE`, no NATS involvement. Safe.
The `EVENTS` stream is created at runtime (not via migration).
The first drainer startup creates the stream with the config
from §"Decision 2". If the stream already exists with a
different config, fail loudly.

---

## Connection to other changes

| Change | Dependency on #8 |
|---|---|
| #1–#6 (base + core domain) | None. Independent. |
| #7 knowledge-compiler | **Direct**: uses `outbox.Record` from day 1. Compiler emits 3 event types. |
| #9 agents-shared-brain | Indirect: agents run via the Compiler, which uses the outbox. |
| #10 hybrid-retrieval-wiring | None. Independent. |
| #11 hnsw-embedding-index | None. Independent. |
| #12 relations-bidirectional-freshness | None. Independent. |
| #14 telegram-validation-ui | Indirect: validation emits `lifecycle.transitioned`. |

After #8 lands:
- #7 (Compiler) can ship and emit events.
- Future `event-subscribers` change (or part of #7) wires the 7
  workers to consume from NATS.
- The `agreement_score` follow-up (after #12) still applies.

---

## What this DOES enable

- **The 7-worker pipeline** (change #7). The workers subscribe
  to `raw_input.received` and process. Without #8, there's no
  bus for them to listen on.
- **At-least-once delivery with idempotency**. Combined with
  §1.2 (NACK) + §1.3 (outbox), the system is effectively
  exactly-once end-to-end.
- **Horizontal scalability**. Multiple drainer processes, multiple
  worker processes. All coordinate via the bus.
- **Observability**. Outbox table = "what should have been
  published by now". DLQ = "what couldn't be processed".
- **Operational separation**. The drainer can be scaled
  independently of the API. The DLQ-watcher is a tiny sidecar.

---

## Spec anchors

- `migrations/0007_embeddings.sql` (referenced for HNSW)
- `internal/config/config.go` (config structure to extend)
- `cmd/api/main.go:148-172` (current shutdown order)
- `internal/app/ingest_text.go:70-186` (IngestTextService)
- `openspec/changes/paradigm-knowledge-os/tasks.md:73-78` — change #8
- `openspec/changes/paradigm-knowledge-os/proposal.md:422-446` — proposal
- `openspec/changes/paradigm-knowledge-os/design.md:120-136` — NATS section
- `openspec/changes/paradigm-knowledge-os/exploration.md:530-538` — exploration
- `research/01-2-nats-nack-semantics.md` — §1.2 decision
- `research/01-3-transactional-outbox.md` — §1.3 decision
