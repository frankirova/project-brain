# Proposal: Ingest Text HTTP API

## Intent

The `project-brain-mvp` delivered a clean three-layer architecture (domain, application, infrastructure) but `cmd/api/main.go` is a bare scaffold — no HTTP server, no handlers. We need a thin HTTP adapter so external consumers (Telegram bot, AI agents) can call `IngestTextService` over the wire. This is the final slice before the knowledge core is usable.

## Scope

### In Scope
- HTTP handler package (`internal/http/`) translating `http.Request` ↔ domain types
- `POST /v1/ingest-text` — accepts `IngestTextRequest` JSON, returns `IngestTextResult` JSON
- `GET /v1/health` — liveness probe returning `{"status":"ok"}`
- Composition root wiring in `cmd/api/main.go` (PostgreSQL if DSN set, in-memory fake if not)
- Graceful shutdown on SIGINT/SIGTERM via `Server.Shutdown`
- Error mapping: `ErrValidation` → 400, `ErrNotFound` → 404, other → 500
- Handler tests using `httptest` and existing `fakeUOW` pattern

### Out of Scope
- Middleware (logging, CORS, recovery, auth, request IDs)
- OpenAPI/Swagger specification
- Additional endpoints (search, relations, agents)
- Structured logging / OpenTelemetry instrumentation
- Telegram bot or Research Agent consumers

## Capabilities

### New Capabilities
- `http-ingest-api`: HTTP adapter layer — handlers, routing, composition root wiring, graceful shutdown, error-to-status mapping for the text ingestion endpoint and health probe

### Modified Capabilities
- None

## Approach

Go stdlib `net/http` with Go 1.22 `ServeMux` (method-based routing). Zero new dependencies — the project already has only `uuid` + `pgx`. The enhanced `ServeMux` handles `POST /v1/ingest-text` natively. A thin handler package (~100–150 LOC) translates between HTTP and domain types. Future consumers (Telegram, agents) call plain HTTP with no framework coupling.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `cmd/api/main.go` | Modified | Composition root: config → DB/fake → service → handler → ServeMux → graceful server |
| `internal/http/` | New | Handler package: `IngestTextHandler`, `HealthHandler`, error mapping, JSON encoding |
| `internal/config/config.go` | Minor | Possible read timeout / shutdown timeout additions |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Graceful shutdown misses in-flight requests | Low | Use `Server.Shutdown` with context timeout; test with `httptest.Server` |
| Error response leaks internal details | Low | Map domain errors to status codes; never expose stack traces or DSN |
| CORS blocks future browser consumers | Low | Defer until a concrete browser consumer exists |

## Rollback Plan

Delete `internal/http/` package, revert `cmd/api/main.go` to the bare scaffold. No data or domain changes to undo — the adapter is purely additive.

## Dependencies

- Go 1.22+ (already in `go.mod`)
- Existing `IngestTextService` and `IngestionUnitOfWork` ports (complete from `project-brain-mvp`)

## Success Criteria

- [ ] `POST /v1/ingest-text` returns 201 with valid `IngestTextResult` JSON
- [ ] `POST /v1/ingest-text` returns 400 on validation errors, 500 on internal errors
- [ ] `GET /v1/health` returns `{"status":"ok"}`
- [ ] Server starts and shuts down gracefully on SIGINT/SIGTERM
- [ ] All handler tests pass with `httptest` and `fakeUOW`
- [ ] Zero new entries in `go.mod` / `go.sum`
