## Exploration: Ingest Text HTTP API

### Current State

The `project-brain-mvp` change is complete and verified. The codebase has a clean three-layer architecture:

- **Domain** (`internal/domain/knowledge.go`): `IngestTextRequest`, `IngestTextResult`, `Source`, `KnowledgeObject`, `AuditEvent`, and related types.
- **Application** (`internal/app/`): `IngestTextService.Ingest(ctx, IngestTextRequest) (IngestTextResult, error)` — a pure use case that delegates persistence through `IngestionUnitOfWork` ports.
- **Infrastructure** (`internal/postgres/`): pgx-backed `DB` implementing `IngestionUnitOfWork`, with repository implementations using transactions.

`cmd/api/main.go` is a bare scaffold: loads config, prints environment/port, exits. No HTTP server, no router, no handlers. `internal/config/config.go` already provides `Port` (default `8080`) and `DatabaseDSN` from environment.

The `IngestTextRequest` and `IngestTextResult` types already have clean JSON-serializable shapes (struct fields map directly to JSON keys). No HTTP-specific adapter exists yet — this is the gap the change must fill.

### Affected Areas

- `cmd/api/main.go` — must start an HTTP server, wire dependencies, register routes
- `internal/config/config.go` — may need minor additions (e.g., read timeout, graceful shutdown config)
- `internal/http/` (new package) — handler layer mapping HTTP ↔ domain types
- `internal/app/ingest_text.go` — no changes needed; the service interface is already clean
- `internal/domain/knowledge.go` — no changes needed; types are already JSON-friendly

### Approaches

1. **Go stdlib `net/http` with Go 1.22 ServeMux** — Use the enhanced `http.ServeMux` introduced in Go 1.22 which supports method-based routing (`POST /v1/ingest-text`) without external dependencies.
   - Pros: Zero new dependencies; aligns with the project's minimal dep footprint (only uuid + pgx); Go 1.22 already in go.mod; stdlib `json`, `http`, `httptest` are battle-tested; method-based routing now native; easy for Telegram bot to call (plain HTTP client); no framework lock-in.
   - Cons: No built-in middleware chain (must write or use `http.Handler` wrapping); no automatic request validation beyond manual `json.Decoder`; more boilerplate for error responses than some frameworks.
   - Effort: Low

2. **Lightweight router (go-chi/chi)** — Add `github.com/go-chi/chi/v5` as a router with middleware support.
   - Pros: Clean middleware chain (logging, recovery, CORS); idiomatic `chi.Handler` interface; small footprint (~1k LOC); well-maintained; good OpenAPI/Swagger ecosystem.
   - Cons: New dependency (even if small); adds `chi` to go.mod; more opinions about route grouping; stdlib 1.22 now covers the primary use case.
   - Effort: Low-Medium

3. **Full framework (Fiber, Gin)** — Use `github.com/gofiber/fiber` or `github.com/gin-gonic/gin` for a batteries-included HTTP layer.
   - Pros: Request binding, validation, middleware ecosystem, Swagger generation out of the box; fast benchmarks.
   - Cons: Heavy dependency tree; opinionated patterns that may not match the existing clean architecture; Fiber wraps Fasthttp (not `net/http`), breaking compatibility with stdlib ecosystem; Gin adds significant surface area for a single-endpoint API; over-engineering for two endpoints.
   - Effort: Medium

### Recommendation

**Go stdlib `net/http` with Go 1.22 ServeMux** — the clear winner for this slice.

Rationale:
- Go 1.22 is already required. The enhanced `ServeMux` supports `POST /v1/ingest-text` natively — the exact routing pattern needed.
- The project has only two external deps (`uuid`, `pgx`). Adding a router for two endpoints violates YAGNI.
- The `IngestTextService` is already decoupled from HTTP. A thin `handler` package translating `http.Request → IngestTextRequest → IngestTextResult → http.Response` is ~100-150 lines.
- Future consumers (Telegram bot, AI agents) call plain HTTP — no framework coupling at the server side matters.
- `httptest` from stdlib is sufficient for handler tests; the existing `fakeUOW` pattern continues to work.
- When more endpoints arrive (search, relations, agents), chi can be adopted later with minimal migration — handlers already return `http.Handler`.

### API Surface

| Method | Path | Purpose | Request Body | Response |
|--------|------|---------|-------------|----------|
| `POST` | `/v1/ingest-text` | Ingest text knowledge | `IngestTextRequest` JSON | `IngestTextResult` JSON |
| `GET` | `/v1/health` | Liveness/readiness probe | — | `{"status":"ok"}` |

### JSON Contract

**POST /v1/ingest-text**

Request:
```json
{
  "workspace_id": "workspace-1",
  "content": "Important knowledge from Telegram",
  "source": {
    "type": "telegram",
    "uri": "https://t.me/mychat/123",
    "external_id": "msg-123",
    "title": "Chat message",
    "idempotency_key": "tg-msg-123",
    "metadata": {"chat_id": "42"},
    "captured_at": "2026-06-09T10:00:00Z"
  },
  "object": {
    "type": "decision",
    "title": "Key decision",
    "summary": "We decided to use stdlib",
    "status": "active",
    "created_by": "user-1",
    "metadata": {"importance": "high"}
  }
}
```

Response (201 Created):
```json
{
  "source_id": "uuid",
  "object_id": "uuid",
  "audit_event_id": "uuid",
  "content_checksum": "sha256...",
  "identity_key": "idem:tg-msg-123",
  "duplicate": false
}
```

Error responses use a consistent shape:
```json
{
  "error": "validation error",
  "message": "workspace_id is required",
  "code": "VALIDATION_ERROR"
}
```

Status codes: 201 (created), 400 (validation), 409 (duplicate — optional, could return 200 with `duplicate: true`), 500 (internal).

### Wiring Strategy

`cmd/api/main.go` becomes the composition root:
1. Load config.
2. If `DatabaseDSN` is set → open PostgreSQL pool, use real `postgres.DB`.
3. If `DatabaseDSN` is empty → use in-memory fake for local dev (the `fakeUOW` from tests, or a simpler in-memory variant).
4. Create `IngestTextService` with the unit of work.
5. Create handler, register routes on `http.ServeMux`.
6. Start `http.Server` with graceful shutdown on SIGINT/SIGTERM.

### Future Consumers

The HTTP API is designed to be called by:
- **Telegram bot**: Sends `POST /v1/ingest-text` when a user forwards/saves a message. The idempotency key ensures safe retries.
- **AI agents (Research Agent)**: Calls the same endpoint to persist structured knowledge discovered during investigation.
- Both consumers benefit from the JSON contract, idempotency key support, and workspace scoping already built into the domain layer.

### Risks

- **Graceful shutdown**: Must handle in-flight requests during SIGTERM. Stdlib supports this via `Server.Shutdown(ctx)` — straightforward but must be wired correctly.
- **Error response contract**: The domain returns `ErrValidation` and `ErrNotFound` as Go errors. The handler must map these to HTTP status codes without leaking internal details.
- **No request validation yet**: The domain validates (empty workspace, empty content), but field format validation (e.g., valid UUIDs in source URI) is deferred. This is acceptable for MVP.
- **CORS**: If the API is called from a browser (unlikely for MVP but possible for admin UI), CORS headers may be needed. Defer unless a concrete consumer requires it.

### Ready for Proposal

Yes — propose `ingest-text-http-api` as a thin HTTP adapter slice over the existing `IngestTextService`. The proposal should state:
- Stdlib `net/http` with Go 1.22 ServeMux as the routing approach.
- Two endpoints: `POST /v1/ingest-text` and `GET /v1/health`.
- Composition root wiring in `cmd/api/main.go` with optional PostgreSQL.
- Graceful shutdown.
- Handler tests using `httptest` and the existing `fakeUOW` pattern.
- Out of scope: middleware (logging, CORS, auth), OpenAPI spec, additional endpoints, request ID propagation, structured logging.
