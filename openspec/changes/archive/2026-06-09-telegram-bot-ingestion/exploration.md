# Exploration: Telegram Bot Ingestion

## Current State

The project has a clean three-layer architecture with clean separation:

- **Domain** (`internal/domain/knowledge.go`): `IngestTextRequest`, `IngestTextResult`, `SourceInput`, `ObjectInput` — already supports `type: "telegram"` source type with metadata fields like `chat_id`
- **Application** (`internal/app/ingest_text.go`): `IngestTextService.Ingest(ctx, IngestTextRequest) (IngestTextResult, error)` — pure use case with idempotency key support
- **Infrastructure**: PostgreSQL persistence via `IngestionUnitOfWork` pattern
- **HTTP Adapter** (`internal/httpapi/handler.go`): Thin adapter translating HTTP ↔ domain types

The service already supports Telegram as a source type (seen in test cases with `type: "telegram"` and `metadata: {"chat_id": "42"}`). The HTTP API exposes `POST /v1/ingest-text` and `GET /v1/health`.

**Composition root** (`cmd/api/main.go`): Currently only starts HTTP server. Needs extension to also start Telegram bot.

**Configuration** (`internal/config/config.go`): Uses `PROJECT_BRAIN_*` env vars. Needs `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` added.

## Affected Areas

- `cmd/api/main.go` — must wire Telegram bot alongside HTTP server, handle graceful shutdown for both
- `internal/config/config.go` — add `TelegramBotToken` config field
- `internal/telegram/` (new package) — bot adapter that translates Telegram updates → `IngestTextRequest`
- `internal/telegram/handler_test.go` — tests using fake service
- `go.mod` — add Telegram bot library dependency

## Library Comparison

| Library | Stars | Deps | Maintained | Handler Pattern | Webhook | Polling | Effort |
|---------|-------|------|------------|-----------------|---------|---------|--------|
| **go-telegram/bot** | 1.7k | Zero | Active (v1.21.0, May 2026) | Context-based, `RegisterHandler` | ✅ | ✅ | Low |
| go-telegram-bot-api | 5.7k | Minimal | Stale (v5.5.1, 2021) | Manual update loop | ✅ | ✅ | Low |
| tucnak/telebot | 4.6k | Minimal | Slow (v3.3, Jun 2024) | Framework, middleware | ✅ | ✅ | Medium |

## Approaches

1. **go-telegram/bot (recommended)**
   - Zero dependencies — aligns with project's minimal dep philosophy (uuid + pgx only)
   - Actively maintained with Bot API 10.0 support (May 2026)
   - Clean context-based handlers: `func(ctx, bot, update)`
   - Built-in handler registration with pattern matching (`/start`, `/help`, default)
   - Supports both polling and webhooks
   - `ProcessUpdate()` method allows manual update processing for testing
   - Pros: Zero deps, modern, clean API, testable
   - Cons: Newer library (1.7k stars vs 5.7k for go-telegram-bot-api)
   - Effort: Low

2. **go-telegram-bot-api/telegram-bot-api**
   - Most popular, battle-tested
   - Simple wrapper — no command routing, manual update loop
   - Stale maintenance (last release 2021)
   - Pros: Large community, stable API
   - Cons: Stale, no built-in routing, more boilerplate
   - Effort: Low

3. **tucnak/telebot**
   - High-level framework with command routing, middleware, keyboards
   - More opinionated, higher abstraction
   - Pros: Rich feature set, good for complex bots
   - Cons: Opinionated patterns may not fit clean architecture, heavier abstraction
   - Effort: Medium

## Recommendation

**go-telegram/bot** — the clear winner for this slice.

Rationale:
- Zero dependencies fits the project's minimal footprint perfectly
- Modern, actively maintained (85 releases, Bot API 10.0)
- Context-based handlers align with Go idioms
- Built-in handler registration handles `/start`, `/help` without custom routing
- `ProcessUpdate()` enables testing without hitting Telegram API
- Both polling and webhook modes supported

## Integration Pattern

The bot adapter should mirror the HTTP handler pattern:

```
internal/telegram/bot.go:
  - NewBotHandler(svc *app.IngestTextService, token string) *BotHandler
  - handleMessage(ctx, bot, update) — extracts text + metadata, calls svc.Ingest()
  - handleStart(ctx, bot, update) — /start command
  - handleHelp(ctx, bot, update) — /help command
  
Message flow:
  1. Receive Telegram update
  2. Extract: message.Text → Content, chat.ID → metadata.chat_id, message.From.ID → metadata.user_id
  3. Build IngestTextRequest with workspace_id (configurable), source.type="telegram", source.external_id=message_id
  4. Call service.Ingest()
  5. Respond: "Saved" or "Duplicate" based on result.Duplicate
```

## Scope Questions

**MVP scope (recommended):**
- Plain message ingestion (any text message → knowledge)
- `/start` — welcome message, no ingestion
- `/help` — usage instructions
- Response: simple confirmation ("Saved" / "Duplicate")

**Out of scope for this change:**
- Forwarded messages, media, stickers
- Group chat handling
- Inline queries
- Webhook setup (use polling for MVP)
- Workspace selection per user/chat
- Authentication/authorization

## Configuration

```go
// internal/config/config.go additions:
TelegramBotToken string  // PROJECT_BRAIN_TELEGRAM_BOT_TOKEN
TelegramMode     string  // PROJECT_BRAIN_TELEGRAM_MODE (polling|webhook, default: polling)
```

**Polling vs Webhook:**
- **MVP: Polling** — simpler, no public URL needed, no TLS setup
- **Production: Webhook** — better for high throughput, but requires public URL + TLS
- The library supports both; start with polling, add webhook option later

## Testing Strategy

The `go-telegram/bot` library enables testing without hitting the real API:

1. **Unit tests**: Create bot handler with fake `IngestTextService` (reuse existing `fakeUOW` pattern)
2. **Manual update processing**: Use `bot.ProcessUpdate(ctx, &update)` to inject test updates
3. **Mock bot responses**: Capture messages sent via bot by intercepting the send method
4. **Integration tests**: Use `UseTestEnvironment()` option for Telegram's test servers

```go
// Test pattern:
func TestHandleMessage_IngestsText(t *testing.T) {
    uow := newFakeUOW()
    svc := app.NewIngestTextService(uow)
    handler := NewBotHandler(svc)
    
    update := &models.Update{
        Message: &models.Message{
            Text: "Important knowledge",
            Chat: &models.Chat{ID: 42},
            From: &models.User{ID: 123},
        },
    }
    
    handler.handleMessage(context.Background(), nil, update)
    
    // Assert: svc was called with correct IngestTextRequest
}
```

## Risks

- **Bot token security**: Must use env var, never commit to repo
- **Rate limiting**: Telegram has limits (30 msgs/sec per bot, 20 msgs/min per chat)
- **Graceful shutdown**: Must handle in-flight bot updates during SIGTERM alongside HTTP server
- **Error handling**: Bot errors (blocked by user, chat not found) must not crash the service
- **Idempotency**: Already handled by `idempotency_key` — use `message_id` as key

## Ready for Proposal

Yes — propose `telegram-bot-ingestion` as a thin Telegram bot adapter slice over the existing `IngestTextService`. The proposal should state:
- `go-telegram/bot` as the library (zero deps, actively maintained)
- Polling mode for MVP, webhook option deferred
- New `internal/telegram/` package with bot handler
- Config extension for `TELEGRAM_BOT_TOKEN` and `TELEGRAM_MODE`
- Graceful shutdown in `cmd/api/main.go`
- Tests using `ProcessUpdate()` with existing `fakeUOW` pattern
- Out of scope: media handling, group chats, inline queries, webhook setup
