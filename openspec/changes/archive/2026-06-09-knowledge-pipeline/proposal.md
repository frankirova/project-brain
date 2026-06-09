# Proposal: Knowledge Pipeline — FTS + §10.1 Schema Alignment

## Intent

Project Brain's `knowledge_objects` table is missing the columns PROJECT_BRAIN.md §10.1 prescribes (`project_id`, `tags`, `confidence`, `importance`) and has no FTS index. Today, ingest performs validate → 4 writes → audit, with zero post-processing. This change adds the storage and indexing foundation needed for Phase 3 (keyword search) of the §16 roadmap **without** changing the synchronous ingestion contract: the FTS index lives in a `GENERATED ALWAYS AS ... STORED` `tsvector` column, so the existing 4-write test stays green and PR scope stays under the 400-line review budget.

## Scope

### In Scope
- New migration adding `project_id`, `tags TEXT[]`, `confidence`, `importance` columns to `knowledge_objects` (all nullable, additive).
- Generated `tsvector` column on `knowledge_objects` (config `'simple'`, source `title || summary || content`) + GIN index.
- Extend `KnowledgeObject` domain struct and `IngestTextRequest` with the new fields.
- Persist the new fields inside the existing `WithinIngestionTx`; **no extra writes** (generated column is updated by Postgres, not the app).
- Keep `TestIngestDoesNotRequireDeferredExternalCapabilities` passing (still exactly 4 application writes per ingest).

### Out of Scope
- Embeddings / pgvector columns (separate `embeddings-pgvector` change).
- Auto-classification, summarization, auto-tagging (caller-supplied for now).
- Related-memory search, relation creation, chunking, entity/claim extraction.
- Async / event-driven pipeline (NATS workers) — §16 Phase 5+.
- Changes to `migrations/0002_relations.sql` (already correct).
- HTTP/Telegram handler contract changes.

## Capabilities

### New Capabilities
- None. This change extends an existing capability; no new `openspec/specs/<name>/` is introduced.

### Modified Capabilities
- `knowledge-core-ingestion`: the schema now exposes `project_id`, `tags`, `confidence`, `importance`, and an FTS index. The 4-write contract is preserved; new fields are nullable so existing callers and data stay valid. Idempotency, workspace scope, and "no external dependencies" requirements are unchanged.

## Approach

Single new migration (`0003_knowledge_objects_fts_and_metadata.sql`) on top of `0001`:

1. `ALTER TABLE knowledge_objects ADD COLUMN project_id UUID NULL` + FK to a future `projects` table (or no FK for MVP — defer).
2. `ALTER TABLE knowledge_objects ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}'` (empty array default keeps inserts cheap).
3. `ALTER TABLE knowledge_objects ADD COLUMN confidence DOUBLE PRECISION NULL`.
4. `ALTER TABLE knowledge_objects ADD COLUMN importance INTEGER NULL CHECK (importance BETWEEN 0 AND 100)`.
5. `ALTER TABLE knowledge_objects ADD COLUMN search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(content,''))) STORED`.
6. `CREATE INDEX knowledge_objects_search_vector_idx ON knowledge_objects USING GIN (search_vector)`.

Go-side: extend `domain.KnowledgeObject` and the ingest request struct; thread the new fields through `knowledgeObjectRepository.Create`. Generated column means **zero new writes** in the application — the FTS index is always consistent with the row, and the existing 4-write test stays green.

Use `'simple'` tsvector config: the project is bilingual ES/EN and `'simple'` is neutral. A future change can add per-object language detection and a query-time config if needed.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `migrations/0003_knowledge_objects_fts_and_metadata.sql` | New | New migration: columns + generated tsvector + GIN index. |
| `internal/domain/knowledge.go` | Modified | Add `ProjectID *uuid.UUID`, `Tags []string`, `Confidence *float64`, `Importance *int` to `KnowledgeObject`. |
| `internal/app/ingest_text.go` | Modified | Accept new fields in `IngestTextRequest`; pass to repo. |
| `internal/postgres/repositories.go` | Modified | `knowledgeObjectRepository.Create` writes the new columns. |
| `openspec/specs/knowledge-core-ingestion/spec.md` | Modified | Delta spec: new schema fields, FTS available, idempotency preserved. |
| `internal/app/ingest_text_test.go` | Modified | Assert new fields persist; keep 4-write assertion. |
| `internal/postgres/ingestion_integration_test.go` | Modified | Assert FTS query returns ingested rows. |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| GIN index write cost at scale | Low | Document tradeoff; MVP volume is tiny. Revisit when row count grows. |
| Generated column breaks on null content | Low | `coalesce(..., '')` guards every component. |
| 4-write test breaks | Low | Generated column is updated by Postgres, not the app — no new application write. |
| `'simple'` config stems Spanish/English poorly | Med | Acceptable for MVP (literal token match); add per-object language config in a later change. |
| Migration order / pgvector confusion | Low | This migration does not touch the vector extension; embeddings are deferred to a separate change. |
| 400-line review budget exceeded | Low | FTS + 4 nullable columns + small Go struct edits = small diff. Defer everything else. |

## Rollback Plan

`0003` is **purely additive** (nullable columns + a generated column + a GIN index). Rollback = `DROP INDEX knowledge_objects_search_vector_idx; ALTER TABLE knowledge_objects DROP COLUMN search_vector, DROP COLUMN importance, DROP COLUMN confidence, DROP COLUMN tags, DROP COLUMN project_id;`. No data loss (existing rows have nulls/empty arrays for the new columns). Application rollback = revert Go struct changes in the same commit chain.

## Dependencies

- PostgreSQL 12+ for `GENERATED ALWAYS AS ... STORED` (the project uses `pgvector/pgvector:pg16` — confirmed).
- `migrations/0001_knowledge_core_ingestion.sql` already applied.

## Success Criteria

- [ ] Migration `0003` applies cleanly on a DB at the `0001` baseline.
- [ ] `INSERT` into `knowledge_objects` automatically populates `search_vector` (verified by integration test selecting via `to_tsquery`).
- [ ] `TestIngestDoesNotRequireDeferredExternalCapabilities` still asserts **exactly 4 application writes** per ingest and passes.
- [ ] `IngestTextRequest` accepts `project_id`, `tags`, `confidence`, `importance`; they round-trip into the persisted `KnowledgeObject`.
- [ ] All existing ingest unit and integration tests pass without behavioral changes.
- [ ] PR diff stays under 400 changed lines.
- [ ] Delta spec added to `openspec/changes/knowledge-pipeline/specs/knowledge-core-ingestion/spec.md`.
