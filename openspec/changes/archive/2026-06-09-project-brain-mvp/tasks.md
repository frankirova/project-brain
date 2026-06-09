# Tasks: Project Brain MVP Knowledge Core Ingestion

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | 650-900 |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 scaffold/tests → PR 2 domain/use case → PR 3 PostgreSQL persistence |
| Delivery strategy | auto-forecast |
| Chain strategy | pending |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: pending
400-line budget risk: High

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Minimal Go module and test runner | PR 1 | `go test ./...` works; no product behavior yet. |
| 2 | Interface-neutral ingestion use case | PR 2 | Domain, validation, idempotency, fake-repo unit tests. |
| 3 | PostgreSQL schema and repository | PR 3 | Migration, transaction persistence, env-gated integration test. |

## Phase 1: Backend Bootstrap

- [x] 1.1 Create `go.mod` for the backend module and baseline dependencies for PostgreSQL, UUIDs, and testing.
- [x] 1.2 Create `cmd/api/main.go` and `internal/config/config.go` with minimal config loading; verify `go test ./...` runs.
- [x] 1.3 Add `README.md` or backend notes with `go test ./...` and optional PostgreSQL DSN verification commands.

## Phase 2: Domain and Ingestion Use Case

- [x] 2.1 Create `internal/domain/knowledge.go` with `Source`, `KnowledgeObject`, `ObjectSource`, `AuditEvent`, request, result, and metadata types.
- [x] 2.2 Create `internal/app/ports.go` with repository and unit-of-work interfaces for transaction-scoped ingestion.
- [x] 2.3 Create `internal/app/ingest_text.go` with workspace/content validation, checksum, identity key computation, duplicate lookup, and transactional orchestration.
- [x] 2.4 Add `internal/app/ingest_text_test.go` covering valid ingestion, whitespace rejection with no writes, duplicate idempotency, and no deferred Telegram/RAG dependencies.

## Phase 3: PostgreSQL Persistence

- [x] 3.1 Create `migrations/0001_knowledge_core_ingestion.sql` for `sources`, `knowledge_objects`, `object_sources`, and `audit_events` with FKs and `UNIQUE(workspace_id, identity_key)`.
- [x] 3.2 Create `internal/postgres/db.go` and repository files implementing unit of work, insert, duplicate lookup, link, audit, and rollback behavior.
- [x] 3.3 Add `internal/postgres/ingestion_integration_test.go` gated by `PROJECT_BRAIN_TEST_DATABASE_DSN`; verify all-or-nothing persistence.

## Phase 4: Verification and Scope Guard

- [x] 4.1 Run `go test ./...`; confirm unit tests pass and integration tests skip cleanly without DSN.
- [x] 4.2 Verify accepted ingestion creates exactly one source, object, link, and audit event; duplicate returns existing result without new audit event.
- [x] 4.3 Confirm no Telegram, embeddings, RAG, NATS, S3, workers, agents, retrieval, or FTS code was introduced.
