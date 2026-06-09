# Design: Ingest Text HTTP API

## Technical Approach

Thin HTTP adapter layer using Go stdlib `net/http` with Go 1.22 `ServeMux` for method-based routing. Zero new dependencies. The handler translates `http.Request` ↔ `domain.IngestTextRequest/Result`, maps domain errors to HTTP status codes, and delegates business logic to the existing `IngestTextService`. Composition root in `cmd/api/main.go` wires config → persistence → service → handler → server.

## Architecture Decisions

### Decision: Routing Approach

| Option | Tradeoff | Decision |
|--------|----------|----------|
| Go 1.22 ServeMux | Zero deps; method routing native; future chi migration trivial | **Chosen** |
| go-chi/chi | Adds dep for middleware chain; YAGNI for 2 endpoints | Rejected |
| Fiber/Gin | Heavy; wraps non-stdlib HTTP; overkill | Rejected |

### Decision: Handler Location

| Option | Tradeoff | Decision |
|--------|----------|----------|
| `internal/http/` package | Follows existing layered structure; clean import path | **Chosen** |
| Inline in `cmd/api/main.go` | Simpler initially; becomes unmaintainable as endpoints grow | Rejected |

### Decision: Error Mapping

| Option | Tradeoff | Decision |
|--------|----------|----------|
| `errors.Is` on domain errors | Explicit; no reflection; type-safe | **Chosen** |
| Interface-based error types | More flexible; adds complexity for 3 error types | Rejected |

## Data Flow

```
POST /v1/ingest-text
  │
  ▼
IngestTextHandler.ServeHTTP()
  ├─ Decode JSON → domain.IngestTextRequest
  ├─ Call IngestTextService.Ingest(ctx, req)
  │    └─ Domain validation → persistence → IngestTextResult
  ├─ On success: encode result, write 201
  ├─ On ErrValidation: write 400 + error JSON
  ├─ On ErrNotFound: write 404 + error JSON
  └─ On other: write 500 + error JSON (no detail leak)

GET /v1/health
  │
  ▼
HealthHandler.ServeHTTP()
  └─ Write {"status":"ok"} with 200
```

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `internal/http/handler.go` | Create | `IngestTextHandler`, `HealthHandler`, error mapping, JSON encode/decode |
| `internal/http/handler_test.go` | Create | Handler tests using `httptest` and `fakeUOW` from `app` package |
| `cmd/api/main.go` | Modify | Composition root: config → DB/fake → service → handler → ServeMux → graceful server |

## Interfaces / Contracts

### Handler Structure

```go
// internal/http/handler.go
type IngestTextHandler struct {
    service *app.IngestTextService
}

func NewIngestTextHandler(svc *app.IngestTextService) *IngestTextHandler
func (h *IngestTextHandler) ServeHTTP(w http.ResponseWriter, r *http.Request)

type HealthHandler struct{}
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

### Request/Response Types

```go
// JSON wire types (internal to handler)
type ingestTextRequest struct {
    WorkspaceID string           `json:"workspace_id"`
    Content     string           `json:"content"`
    Source      domain.SourceInput `json:"source"`
    Object      domain.ObjectInput `json:"object"`
}

type errorResponse struct {
    Error   string `json:"error"`
    Message string `json:"message"`
    Code    string `json:"code"`
}
```

### Composition Root

```go
// cmd/api/main.go — key wiring
mux := http.NewServeMux()
mux.Handle("POST /v1/ingest-text", http.NewIngestTextHandler(svc))
mux.Handle("GET /v1/health", &http.HealthHandler{})

server := &http.Server{Addr: ":" + cfg.Port, Handler: mux}
// Graceful shutdown via signal.NotifyContext + server.Shutdown(ctx)
```

## Testing Strategy

| Layer | What to Test | Approach |
|-------|-------------|----------|
| Unit | Handler JSON decode/encode, error mapping | `httptest.NewRecorder`, fake service |
| Integration | Full request path through handler → service → fakeUOW | `httptest.NewServer` with real handler + fakeUOW (reuse `app` test doubles) |
| E2E | Server startup, graceful shutdown | `httptest.Server` with signal simulation (optional) |

Handler tests reuse the `fakeUOW` pattern from `internal/app/ingest_text_test.go` — promoted to a shared test helper or duplicated in `internal/http/` (prefer duplication for isolation).

## Migration / Rollout

No migration required. This is a pure additive change — new HTTP layer over existing service. Revert by deleting `internal/http/` and restoring the bare `cmd/api/main.go` scaffold.

## Open Questions

- None — all decisions aligned with exploration, proposal, and spec.
