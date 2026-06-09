# Proposal: Telegram Bot Ingestion

## Intent

Add a Telegram bot adapter that receives text messages from Telegram users and ingests them into the Knowledge Core via the existing `IngestTextService`. This enables users to send knowledge directly from Telegram without requiring HTTP API calls.

## Scope

### In Scope
- Telegram bot handler that receives text messages and calls `IngestTextService.Ingest()`
- `/start` command with welcome message
- `/help` command with usage instructions
- Simple confirmation responses ("Saved" / "Duplicate")
- Configuration for bot token and polling/webhook mode
- Graceful shutdown alongside HTTP server
- Unit tests using `ProcessUpdate()` with fake service

### Out of Scope
- Media handling (photos, files, stickers)
- Group chat handling (only private chats for MVP)
- Inline queries
- Webhook setup (polling only for MVP)
- Workspace selection per user/chat
- Authentication/authorization
- Rate limiting implementation (use Telegram defaults)

## Capabilities

### New Capabilities
- `telegram-bot-adapter`: Telegram bot that receives text messages and ingests them into Knowledge Core via existing `IngestTextService`

### Modified Capabilities
None — this change adds a new adapter over existing `knowledge-core-ingestion` capability.

## Approach

Use `go-telegram/bot` library (zero dependencies, actively maintained) with polling mode for MVP. Create a thin adapter in `internal/telegram/` that mirrors the HTTP handler pattern:

1. Receive Telegram update
2. Extract text, chat ID, user ID, message ID
3. Build `IngestTextRequest` with `source.type="telegram"` and `metadata.chat_id`
4. Call `IngestTextService.Ingest()` with `idempotency_key=message_id`
5. Respond with confirmation

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `cmd/api/main.go` | Modified | Wire Telegram bot alongside HTTP server, handle graceful shutdown for both |
| `internal/config/config.go` | Modified | Add `TelegramBotToken` and `TelegramMode` config fields |
| `internal/telegram/` | New | Bot adapter package with handler and tests |
| `go.mod` | Modified | Add `go-telegram/bot` dependency |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Bot token security | Low | Use env var only, never commit to repo |
| Rate limiting | Medium | Use `message_id` as idempotency key, let Telegram handle rate limits |
| Graceful shutdown | Low | Follow existing HTTP server shutdown pattern |
| Error handling | Low | Wrap service errors, log but don't crash on user errors |

## Rollback Plan

1. Remove `internal/telegram/` package
2. Remove Telegram config fields from `internal/config/config.go`
3. Remove Telegram bot wiring from `cmd/api/main.go`
4. Remove `go-telegram/bot` from `go.mod`
5. Rebuild and redeploy HTTP-only service

## Dependencies

- Existing `IngestTextService` with `source.type="telegram"` support
- `go-telegram/bot` library (zero dependencies)
- `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` environment variable

## Success Criteria

- [ ] Bot receives Telegram messages and ingests text into Knowledge Core
- [ ] `/start` and `/help` commands respond correctly
- [ ] Unit tests pass with fake service
- [ ] Graceful shutdown handles in-flight updates
- [ ] No regression in existing HTTP API functionality
