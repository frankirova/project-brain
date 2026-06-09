# Design: Project Brain MVP Knowledge Core Ingestion

## Technical Approach

Bootstrap a minimal Go backend around one interface-neutral ingestion use case. The core accepts plain text plus workspace/source metadata, validates it, then persists `source`, `knowledge_object`, `object_sources`, and `audit_event` in one PostgreSQL transaction. No Telegram, embeddings, RAG, NATS, S3, agents, or retrieval code is introduced in this slice.

## Architecture Decisions

| Decision | Choice | Alternatives | Rationale / Consequence |
|---|---|---|---|
| Backend shape | Go module with `cmd/api`, `internal/app`, `internal/domain`, `internal/postgres`, `internal/config` | Flat scripts or channel-first bot | Gives future adapters a stable boundary while keeping this slice small. API command wiring may be minimal, but domain/use case must exist. |
| Persistence | PostgreSQL-first migrations for the four required tables | In-memory MVP, document DB, graph DB | Matches Project Brain’s auditability and transaction needs. Graph/vector tables stay deferred. |
| Use case boundary | `IngestText` service depends on repository interfaces and transaction boundary | Repository-only CRUD | Preserves platform logic outside adapters; prevents Telegram/API concerns shaping the core. |
| Idempotency | Effective source identity = provided idempotency key, else SHA-256 over normalized `workspace_id`, `source.type`, source locator (`uri` or external id when present), and content checksum. Store as `sources.identity_key`; unique on `(workspace_id, identity_key)`. | Content-only checksum; random UUID only | Checksum-based identity makes retries safe while allowing same text from distinct sources to create separate records. Duplicate returns the existing ingestion result and creates no new audit event. |

## Data Flow

```text
Adapter/Test command
  -> IngestText request
  -> validate workspace + non-empty text
  -> compute content_checksum + identity_key
  -> transaction
     -> find source by workspace_id + identity_key
     -> if found: load linked object and return existing result
     -> insert source
     -> insert knowledge_object
     -> insert object_sources
     -> insert audit_event(action='knowledge.ingested')
  -> result {source_id, object_id, audit_event_id, duplicate}
```

## File Changes

| File | Action | Description |
|---|---|---|
| `go.mod` | Create | Go module and baseline dependencies. |
| `cmd/api/main.go` | Create | Minimal executable that loads config; no channel-specific behavior. |
| `internal/domain/knowledge.go` | Create | Source, KnowledgeObject, ObjectSource, AuditEvent, ingestion request/result types. |
| `internal/app/ingest_text.go` | Create | Validation, idempotency, transaction orchestration. |
| `internal/app/ports.go` | Create | Repository and transaction interfaces. |
| `internal/postgres/*.go` | Create | PostgreSQL implementations of repositories/unit of work. |
| `migrations/0001_knowledge_core_ingestion.sql` | Create | Tables, constraints, indexes. |
| `internal/app/ingest_text_test.go` | Create | Unit tests with fake repositories. |
| `internal/postgres/ingestion_integration_test.go` | Create | Optional PostgreSQL integration test gated by env/config. |

## Interfaces / Contracts

```go
type IngestTextRequest struct {
    WorkspaceID string
    Content string
    Source SourceInput // Type, URI, ExternalID, Title, CapturedAt, Metadata, IdempotencyKey
    Object ObjectInput // Type, Title, Summary, Status, Metadata, CreatedBy
}
```

Migration tables: `sources(id, workspace_id, type, uri, external_id, title, checksum, identity_key, metadata, captured_at)`, `knowledge_objects(...)`, `object_sources(object_id, source_id, relevance)`, `audit_events(...)`. Add `UNIQUE(workspace_id, identity_key)` and FK constraints for links.

## Testing Strategy

| Layer | What to Test | Approach |
|---|---|---|
| Unit | validation, checksum identity, duplicate behavior, transaction orchestration | `go test ./...` with fakes; first implementation bootstraps runner. |
| Integration | migrations and all-or-nothing PostgreSQL persistence | Env-gated test using a real PostgreSQL DSN; skipped when unavailable. |
| E2E | None in first slice | No public interface is required yet. |

## Migration / Rollout

No production migration required. Implementation creates first schema migration and applies it only in development/test environments until deployment exists.

## Open Questions

- None blocking.
