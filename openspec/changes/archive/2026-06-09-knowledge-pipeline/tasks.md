# Tasks: Knowledge Pipeline — FTS + §10.1 Schema Alignment

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~120 (1 new migration ~30 lines; 4 modified Go files ~70 lines; 2 modified tests ~20 lines) |
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
| 1 | Schema + FTS index, domain struct extension, request/repo wiring, tests | PR 1 | Single self-contained slice; migration first, then Go (struct → service → repo), then tests. No follow-up PRs. |

## Phase 1: Schema Foundation (Migration)

- [x] 1.1 Create `migrations/0003_knowledge_objects_fts_and_metadata.sql` mirroring the additive style of `0002_relations.sql` (idempotent `ADD COLUMN IF NOT EXISTS`, `CHECK` on `importance`, generated `tsvector` column, GIN index).
- [x] 1.2 Add `project_id UUID`, `tags TEXT[] NOT NULL DEFAULT '{}'`, `confidence DOUBLE PRECISION`, `importance INTEGER CHECK (between 0 and 100)` to `knowledge_objects` — all additive / nullable except `tags` default.
- [x] 1.3 Add generated column `search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(content,''))) STORED`.
- [x] 1.4 Create `INDEX knowledge_objects_search_vector_idx ON knowledge_objects USING GIN (search_vector)` (use `IF NOT EXISTS`).
- [x] 1.5 Manually verify SQL applies cleanly on a fresh DB at the `0001` baseline, and that running it twice is a no-op.

## Phase 2: Domain Struct Extension

- [x] 2.1 In `internal/domain/knowledge.go`, add `ProjectID *uuid.UUID`, `Tags []string`, `Confidence *float64`, `Importance *int` to `KnowledgeObject` (existing fields untouched; pointer types match the existing `Relation.Confidence *float64` pattern).
- [x] 2.2 Add the same four fields (with `json:"...,omitempty"` tags) to `ObjectInput` so `IngestTextRequest.Object` carries them through to the service.
- [x] 2.3 Default `Tags` to a non-nil empty slice in `prepareIngestText` when callers pass nil, to keep array writes predictable.

## Phase 3: Ingest Service Wiring

- [x] 3.1 In `internal/app/ingest_text.go`, populate the four new fields on the constructed `domain.KnowledgeObject` from `prepared.object.*` — **no new writes** are added; the FTS index is database-internal.
- [x] 3.2 Do NOT touch the `WithinIngestionTx` write order (`Sources` → `KnowledgeObjects` → `ObjectSources` → `AuditEvents`); keep call signatures identical.

## Phase 4: Repository Insert Extension

- [x] 4.1 In `internal/postgres/repositories.go`, extend the `knowledgeObjectRepository.Create` `INSERT` column list and `VALUES` placeholders to include `project_id`, `tags`, `confidence`, `importance`.
- [x] 4.2 Add small nullable helpers for `*uuid.UUID` → `pgtype`/driver-null, `*float64` → nullable numeric, `*int` → nullable int, alongside the existing `nullableString` helper; pass `Tags` as a Go `[]string` directly (pgx maps `TEXT[]` natively).
- [x] 4.3 Do NOT add a second write (e.g. a metadata-update or FTS-touch); the generated column is updated by Postgres.

## Phase 5: Tests — Unit

- [x] 5.1 In `internal/app/ingest_text_test.go`, add a sub-case to `TestIngestDoesNotRequireDeferredExternalCapabilities` that supplies non-nil `ProjectID`, `Tags`, `Confidence`, `Importance`; assert `writeCount()` remains `4` and `uow.repos.object.created[0]` round-trips the new fields.
- [x] 5.2 Add a defaulting test asserting that `IngestTextRequest{Object.ObjectInput}` with nil `Tags` produces a non-nil empty slice on the persisted `KnowledgeObject`.
- [x] 5.3 Keep `TestIngestRejectsWhitespaceWithoutWrites` and `TestIngestReturnsDuplicateWithoutCreatingRecords` passing without modification (no contract drift).

## Phase 6: Tests — Integration (PostgreSQL)

- [x] 6.1 In `internal/postgres/ingestion_integration_test.go`, extend `openIntegrationDB` to apply `0003_knowledge_objects_fts_and_metadata.sql` after `0001` and `0002`.
- [x] 6.2 Add `TestPostgresIngestionFTSAutoPopulated` — insert a row with distinctive title/summary/content words, then `SELECT count(*) FROM knowledge_objects WHERE search_vector @@ to_tsquery('simple','<word>')` for each of the three fields, asserting the row matches.
- [x] 6.3 Add `TestPostgresIngestionImportanceCheckRejects200` — call `Create` with `Importance=200`, expect a `SQLSTATE 23514` (check_violation) error.
- [x] 6.4 Keep `TestPostgresIngestionPersistsAndDeduplicates` and `TestPostgresIngestionRollsBackPartialWrites` passing by asserting the new fields persist (extend `assertWorkspaceCounts` if needed; do NOT break the existing count semantics).

## Phase 7: Spec Reconciliation (Archive-Time, Not Apply)

- [x] 7.1 Flag for the archive phase: at archive, narrow `### Requirement: Exclude Retrieval and Channel Behavior` in `openspec/specs/knowledge-core-ingestion/spec.md` — drop only FTS from the exclusion list (per design decision #6). This is an archive-phase action, NOT an apply-phase task; noted here for traceability only.

## Phase 8: Self-Verification Before PR

- [x] 8.1 Run `go test ./...` and confirm all unit and integration tests pass (with `PROJECT_BRAIN_TEST_DATABASE_DSN` set).
- [x] 8.2 Run `gofmt -l` and `go vet ./...` — no diffs, no warnings.
- [x] 8.3 Confirm `git diff --stat` against the base branch is under 400 changed lines before opening the PR.
