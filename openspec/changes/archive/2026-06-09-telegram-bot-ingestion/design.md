# Design: Telegram Bot Ingestion

## Technical Approach

Add a Telegram bot adapter as a new transport layer alongside the existing HTTP adapter. The adapter mirrors the HTTP handler pattern: receive Telegram update → extract fields → build `IngestTextRequest` → call `IngestTextService.Ingest()` → send response. The bot runs in polling mode, coexists with the HTTP server, and shares the same `IngestTextService` instance.

## Architecture Decisions

### Decision: Library Choice — go-telegram/bot

| Option | Tradeoff | Decision |
|--------|----------|----------|
| go-telegram/bot | Zero deps, actively maintained, clean API | ✅ Selected |
| telegram-bot-api | More popular but pulls in extra deps | Rejected |
| raw HTTP calls | Full control but high maintenance burden | Rejected |

**Rationale**: go-telegram/bot aligns with the project's minimal-deps philosophy (only pgx + uuid currently). Zero external dependencies means no transitive risk. The library's `ProcessUpdate()` API enables clean unit testing with fake services.

### Decision: Adapter Pattern — Mirror HTTP Handler

| Option | Tradeoff | Decision |
|--------|----------|----------|
| New `internal/telegram/` package | Parallel structure to `internal/httpapi/`, clear separation | ✅ Selected |
| Inline in `cmd/api/main.go` | Simpler but violates SRP | Rejected |
| Shared adapter interface | Premature abstraction for 2 transport types | Rejected |

**Rationale**: Follows existing `internal/httpapi/handler.go` pattern exactly. Each adapter lives in its own package, takes `*app.IngestTextService` via constructor, handles transport-specific concerns. Consistent with the codebase's current architecture.

### Decision: Idempotency — message_id as IdempotencyKey

| Option | Tradeoff | Decision |
|--------|----------|----------|
| Use `source.IdempotencyKey = message_id` | Leverages existing service dedup via `computeIdentityKey` | ✅ Selected |
| Use `source.ExternalID = message_id` | Would need additional dedup logic | Rejected |
| In-memory seen-set | Loses dedup across restarts | Rejected |

**Rationale**: The existing `IngestTextService` already computes identity keys using `source.IdempotencyKey` when present (see `computeIdentityKey` in `ingest_text.go:187`). Passing `message_id` as the idempotency key gives us dedup for free with zero new code.

### Decision: Workspace ID — Hardcoded Default

| Option | Tradeoff | Decision |
|--------|----------|----------|
| Hardcode `"default"` workspace | Simple MVP, no per-user workspace selection | ✅ Selected |
| Per-chat workspace mapping | Complex, needs storage for MVP | Deferred |
| Config env var | Single workspace only | Rejected |

**Rationale**: Per-chat workspace selection is explicitly out of scope per proposal. Hardcoding `"default"` matches the existing HTTP test pattern and can be upgraded later without API changes.

## Data Flow

```
Telegram User
     │
     ▼
go-telegram/bot (polling)
     │
     ▼
internal/telegram/handler.go
  ├─ /start → respond "Welcome" (no service call)
  ├─ /help  → respond "Instructions" (no service call)
  └─ text   → build IngestTextRequest
              │
              ▼
         IngestTextService.Ingest()
              │
              ▼
         IngestionUnitOfWork → Source, Object, Link, Audit
              │
              ▼
         Result.Duplicate? → "Duplicate" : "Saved"
              │
              ▼
         Telegram reply to user
```

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/telegram/handler.go` | Create | Bot adapter: `Handler` struct, `ProcessUpdate()`, command routing, response formatting |
| `internal/telegram/handler_test.go` | Create | Unit tests with fake `IngestTextService` — command handling, ingestion, duplicates, errors |
| `internal/config/config.go` | Modify | Add `TelegramBotToken string` field, read `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` env var |
| `cmd/api/main.go` | Modify | Wire bot: create handler, start polling goroutine, add to shutdown sequence |
| `go.mod` | Modify | Add `github.com/go-telegram/bot` dependency |

## Interfaces / Contracts

### Telegram Handler

```go
// internal/telegram/handler.go
package telegram

type Handler struct {
    service *app.IngestTextService
}

func NewHandler(svc *app.IngestTextService) *Handler

// ProcessUpdate handles a single Telegram update.
// Returns nil for commands (/start, /help) or on successful ingestion.
// Returns error only for unexpected failures (logged, not sent to user).
func (h *Handler) ProcessUpdate(ctx context.Context, update bot.Update) error
```

### Config Extension

```go
// Added to Config struct in internal/config/config.go
TelegramBotToken string  // PROJECT_BRAIN_TELEGRAM_BOT_TOKEN
```

### Request Mapping

```go
// Telegram update → IngestTextRequest
domain.IngestTextRequest{
    WorkspaceID: "default",
    Content:     update.Message.Text,
    Source: domain.SourceInput{
        Type:           "telegram",
        ExternalID:     strconv.Itoa(update.Message.ID),
        IdempotencyKey: strconv.Itoa(update.Message.ID),
        Metadata: domain.Metadata{
            "chat_id": update.Message.Chat.ID,
            "user_id": update.Message.From.ID,
        },
    },
    Object: domain.ObjectInput{
        Type:      "document",
        CreatedBy: strconv.Itoa(update.Message.From.ID),
    },
}
```

## Testing Strategy

| Layer | What to Test | Approach |
|-------|-------------|----------|
| Unit | Command handling (/start, /help) | Fake service, assert response text via `bot.SendMessage` mock |
| Unit | Text ingestion → "Saved" | Fake service, verify `Ingest()` called with correct request fields |
| Unit | Duplicate → "Duplicate" | Fake service returning `Duplicate=true`, verify no service call |
| Unit | Service error → log + generic response | Fake service returning error, verify bot doesn't crash |
| Unit | Missing token → skip bot start | Config with empty token, verify no polling goroutine |
| Integration | Graceful shutdown with both servers | End-to-end with real config, SIGTERM during polling |

## Migration / Rollout

No data migration required. This is an additive change — new adapter over existing service.

**Feature flag**: Bot only starts if `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` is set. Missing token = HTTP-only mode. This provides safe rollout: deploy first, configure token second.

## Open Questions

- [ ] Should we add a `PROJECT_BRAIN_TELEGRAM_WORKSPACE_ID` env var for configurable default workspace, or keep hardcoded `"default"`?
- [ ] Rate limiting: rely on Telegram's built-in limits or add explicit throttling in the adapter?
