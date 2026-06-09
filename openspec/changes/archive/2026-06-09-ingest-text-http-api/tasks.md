# Tasks: Ingest Text HTTP API

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | 200–280 |
| 400-line budget risk | Low |
| Chained PRs recommended | No |
| Suggested split | Single PR |
| Delivery strategy | auto-chain |
| Chain strategy | pending |

Decision needed before apply: No
Chained PRs recommended: No
Chain strategy: pending
400-line budget risk: Low

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | HTTP handler + tests + composition root wiring | PR 1 | Single PR, ~250 lines, self-contained |

## Phase 1: Handler Package

- [x] 1.1 Create `internal/http/handler.go` — package declaration, imports (`net/http`, `encoding/json`, `errors`, `github.com/frankirova/project-brain/internal/app`, `github.com/frankirova/project-brain/internal/domain`)
- [x] 1.2 Define `IngestTextHandler` struct holding `*app.IngestTextService`; constructor `NewIngestTextHandler(svc) *IngestTextHandler`
- [x] 1.3 Define JSON wire types: `ingestTextRequest` (WorkspaceID, Content, Source as `domain.SourceInput`, Object as `domain.ObjectInput`) and `errorResponse` (Error, Message, Code strings)
- [x] 1.4 Implement `IngestTextHandler.ServeHTTP` — decode JSON → `domain.IngestTextRequest` → call `service.Ingest` → on success write 201 + `IngestTextResult` JSON with `Content-Type: application/json`
- [x] 1.5 Implement error mapping in `ServeHTTP`: `errors.Is(err, app.ErrValidation)` → 400 `VALIDATION_ERROR`, `errors.Is(err, app.ErrNotFound)` → 404 `NOT_FOUND`, other → 500 `INTERNAL_ERROR`; use `errorResponse` struct, never leak internals
- [x] 1.6 Define `HealthHandler` struct; `ServeHTTP` writes `{"status":"ok"}` with 200

## Phase 2: Handler Tests

- [x] 2.1 Create `internal/http/handler_test.go` — duplicate `fakeUOW` + fake repos from `internal/app/ingest_text_test.go` for isolation (design decision: prefer duplication over shared test helper)
- [x] 2.2 Test: valid `IngestTextRequest` → 201, response contains `source_id`, `object_id`, `audit_event_id`, `content_checksum`, `identity_key`, `duplicate: false`
- [x] 2.3 Test: missing `workspace_id` or empty `content` → 400 with `VALIDATION_ERROR` code
- [x] 2.4 Test: service returns `ErrNotFound` → 404 with `NOT_FOUND` code
- [x] 2.5 Test: service returns arbitrary error → 500 with `INTERNAL_ERROR` code, no internal detail in body
- [x] 2.6 Test: duplicate request (service returns `Duplicate: true`) → 201 with `duplicate: true`
- [x] 2.7 Test: `GET /v1/health` → 200 with `{"status":"ok"}`
- [x] 2.8 Test: malformed JSON body → 400 (decode error mapped to validation-style response)

## Phase 3: Composition Root Wiring

- [x] 3.1 Modify `cmd/api/main.go` — keep existing `config.Load()` + error handling; add imports for `context`, `net/http`, `os/signal`, `syscall`, `time`, `internal/app`, `internal/http`, `internal/postgres`
- [x] 3.2 Implement persistence selection: if `cfg.DatabaseDSN != ""` → `postgres.Open(ctx, dsn)` + `defer db.Close()`; else → use in-memory `fakeUOW` (promote or duplicate from handler tests)
- [x] 3.3 Wire `app.NewIngestTextService(uow)` → `http.NewIngestTextHandler(svc)` → register routes on `http.NewServeMux`: `"POST /v1/ingest-text"` and `"GET /v1/health"`
- [x] 3.4 Create `http.Server{Addr: ":" + cfg.Port, Handler: mux}`; start in goroutine
- [x] 3.5 Implement graceful shutdown: `signal.NotifyContext` for `SIGINT`/`SIGTERM` → `server.Shutdown(ctx)` with 5-second timeout → close DB if applicable → `os.Exit(0)`

## Phase 4: Verify

- [x] 4.1 Run `go build ./cmd/api` — confirms compilation with zero new `go.mod` entries
- [x] 4.2 Run `go test ./internal/http/` — all handler tests pass
- [x] 4.3 Run `go test ./...` — no regressions in existing app/postgres/domain tests
- [ ] 4.4 Manual smoke test: `go run ./cmd/api` → `curl -X POST localhost:8080/v1/ingest-text -d '...'` returns 201; `curl localhost:8080/v1/health` returns `{"status":"ok"}`
