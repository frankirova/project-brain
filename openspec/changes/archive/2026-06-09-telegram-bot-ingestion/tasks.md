# Tasks: Telegram Bot Ingestion

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~150 |
| 400-line budget risk | Low |
| Chained PRs recommended | No |
| Suggested split | single PR |
| Delivery strategy | single-pr |
| Chain strategy | pending |

Decision needed before apply: No
Chained PRs recommended: No
Chain strategy: pending
400-line budget risk: Low

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Telegram bot adapter + wiring + tests | PR 1 | ~150 lines, well under budget. Config + handler + main wiring + unit tests. |

## Phase 1: Foundation

- [x] 1.1 Add `TelegramBotToken string` field to `Config` struct in `internal/config/config.go`, read `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` env var in `Load()`
- [x] 1.2 Run `go get github.com/go-telegram/bot` to add dependency to `go.mod`

## Phase 2: Core Implementation

- [x] 2.1 Create `internal/telegram/handler.go` — `Handler` struct with `service *app.IngestTextService`, `NewHandler(svc)` constructor, and `ProcessUpdate(ctx, update) error` method
- [x] 2.2 Implement command routing in `ProcessUpdate`: `/start` → reply "Welcome" (no service call), `/help` → reply usage instructions (no service call)
- [x] 2.3 Implement text ingestion path: extract text/chat_id/user_id/message_id from `update.Message`, build `domain.IngestTextRequest` with `source.type="telegram"`, `source.IdempotencyKey=message_id`, `metadata.chat_id`, workspace_id `"default"`, call `service.Ingest()`, reply "Saved" or "Duplicate"
- [x] 2.4 Wire bot in `cmd/api/main.go`: create `telegram.NewHandler(svc)`, add conditional bot start (skip if token empty), add polling goroutine, extend shutdown sequence with bot stop

## Phase 3: Testing

- [x] 3.1 Create `internal/telegram/handler_test.go` — fake `IngestTextService` implementing `Ingest()` that records calls and returns configurable results
- [x] 3.2 Test `/start` command: verify welcome response text, verify no service call
- [x] 3.3 Test `/help` command: verify instructions response text, verify no service call
- [x] 3.4 Test text ingestion: verify `Ingest()` called with correct request fields (`source.type="telegram"`, `source.IdempotencyKey`, `metadata.chat_id`), verify "Saved" response
- [x] 3.5 Test duplicate: fake service returns `Duplicate=true`, verify "Duplicate" response
- [x] 3.6 Test service error: fake service returns error, verify bot logs error and replies generic message without crashing
