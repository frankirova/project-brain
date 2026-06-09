# Design: Knowledge Pipeline — FTS + §10.1 Schema Alignment

## Technical Approach

Extend `knowledge_objects` with the four columns PROJECT_BRAIN.md §10.1 prescribes (`project_id`, `tags`, `confidence`, `importance`) and a `GENERATED ALWAYS AS ... STORED` `tsvector` column (`to_tsvector('simple', title || summary || content)`) backed by a GIN index. Thread the new fields through the domain struct, request struct, and `knowledgeObjectRepository.Create`. The generated column is computed by Postgres, so the **4-write contract per ingest is preserved** — FTS consistency is a database-level invariant, not an application responsibility. Follows the existing additive migration pattern (`0002_relations.sql` is the template).

## Architecture Decisions

| # | Decision | Choice | Tradeoff / Alternative rejected |
|---|----------|--------|---------------------------------|
| 1 | FTS column shape | `GENERATED ALWAYS AS ... STORED` tsvector, GIN index | (a) trigger-maintained — hidden write breaks 4-write test; (b) app-side `to_tsvector` on read — can't use GIN; (c) denormalized FTS table — doubles writes |
| 2 | tsvector config | `'simple'` (lower + tokenize only) | `'spanish'`/`'english'` aggressively stem and corrupt exact match + mishandle bilingual docs; per-row `regconfig` adds nullable complexity not in any current spec |
| 3 | Nullability of §10.1 columns | `project_id`, `confidence`, `importance` NULL; `tags TEXT[] NOT NULL DEFAULT '{}'` | Mandatory + sentinel defaults break existing callers; packing into `metadata` JSONB hides structure and prevents GIN-friendly queries |
| 4 | `importance` bounds | `CHECK (importance BETWEEN 0 AND 100)` | Matches existing `object_sources.relevance` CHECK pattern; app-layer-only validation gets bypassed by direct SQL |
| 5 | Repository topology | Extend `KnowledgeObjectRepository.Create`; no new port | Separate `FullTextSearchRepository` or split metadata-update would force 2 writes and break 4-write contract |
| 6 | "Exclude Retrieval and Channel" reconciliation | At archive, **narrow** the requirement — drop only FTS from the exclusion list | Delete the requirement → loses a load-bearing invariant; leave the conflict → corrupts merged main spec |

## Data Flow

```
Caller (HTTP / Telegram / future channel)
  │ IngestTextRequest{ProjectID, Tags, Confidence, Importance}
  ▼
IngestTextService.Ingest → prepareIngestText → WithinIngestionTx
  ├── Sources().Create          ← write #1
  ├── KnowledgeObjects().Create ← write #2  (Postgres auto-fills search_vector)
  ├── ObjectSources().Create    ← write #3
  └── AuditEvents().Create      ← write #4
```

Application writes per ingest: **exactly 4**. FTS index update is a Postgres internal, not counted.

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `migrations/0003_knowledge_objects_fts_and_metadata.sql` | Create | `ALTER TABLE` adds 4 columns + generated `tsvector` + GIN index. |
| `internal/domain/knowledge.go` | Modify | Add `ProjectID *uuid.UUID`, `Tags []string`, `Confidence *float64`, `Importance *int` to `KnowledgeObject`; same fields (with JSON tags) on `ObjectInput`/`IngestTextRequest`. |
| `internal/app/ingest_text.go` | Modify | Pass new fields from `req.Object` into the constructed `KnowledgeObject`. No new writes. |
| `internal/postgres/repositories.go` | Modify | Extend `knowledgeObjectRepository.Create` INSERT column list + args; nullable helpers for `*uuid.UUID`, `*float64`, `*int`. |
| `openspec/specs/knowledge-core-ingestion/spec.md` | Modify (at archive) | Narrow `### Requirement: Exclude Retrieval and Channel Behavior` — drop FTS from the exclusion list. |
| `internal/app/ingest_text_test.go` | Modify | Keep `TestIngestDoesNotRequireDeferredExternalCapabilities` unchanged. Add a case asserting new fields round-trip through the fake UoW. |
| `internal/postgres/ingestion_integration_test.go` | Modify | Insert a row, then `SELECT ... WHERE search_vector @@ to_tsquery('simple', 'word')` to confirm auto-population. |

## Interfaces / Contracts

Go-side additions (existing fields preserved):

```go
type KnowledgeObject struct {
    // ... existing fields unchanged ...
    ProjectID  *uuid.UUID  // nullable; no FK yet
    Tags       []string    // always non-nil on read
    Confidence *float64    // nullable
    Importance *int        // nullable; CHECK 0..100 in DB
}

type ObjectInput struct {
    // ... existing fields unchanged ...
    ProjectID  *uuid.UUID `json:"project_id,omitempty"`
    Tags       []string   `json:"tags,omitempty"`
    Confidence *float64   `json:"confidence,omitempty"`
    Importance *int       `json:"importance,omitempty"`
}
```

SQL (the non-obvious piece — generated-column expression):

```sql
ALTER TABLE knowledge_objects
    ADD COLUMN IF NOT EXISTS project_id    UUID,
    ADD COLUMN IF NOT EXISTS tags          TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS confidence    DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS importance    INTEGER
        CHECK (importance IS NULL OR (importance BETWEEN 0 AND 100)),
    ADD COLUMN IF NOT EXISTS search_vector tsvector
        GENERATED ALWAYS AS (to_tsvector('simple',
            coalesce(title,'')||' '||coalesce(summary,'')||' '||coalesce(content,'')
        )) STORED;
CREATE INDEX IF NOT EXISTS knowledge_objects_search_vector_idx
    ON knowledge_objects USING GIN (search_vector);
```

## Testing Strategy

| Layer | What to Test | Approach |
|-------|-------------|----------|
| Unit | Defaults (Tags nil→`[]`, others nil→nil) | Table-driven on `IngestTextRequest` |
| Unit | 4-write contract holds with new fields populated | `TestIngestDoesNotRequireDeferredExternalCapabilities` extended with non-nil new fields; `writeCount()` must remain 4 |
| Unit | New fields round-trip via fake UoW | Inspect `uow.repos.object.created[0]` |
| Integration | GIN auto-populated on INSERT | Insert content; `SELECT count(*) WHERE search_vector @@ to_tsquery('simple','word')` = 1 |
| Integration | `importance` CHECK rejects 200 | Expect `SQLSTATE 23514` |
| Integration | Generated column covers title, summary, content | Insert distinctive words in each; each matches via `to_tsquery` |
| Migration | `0003` applies cleanly + is idempotent | Run migration twice |

## Migration / Rollout

Purely additive: 4 nullable columns, 1 generated column, 1 GIN index. No data backfill. **Rollback** (single tx):
```sql
DROP INDEX IF EXISTS knowledge_objects_search_vector_idx;
ALTER TABLE knowledge_objects
    DROP COLUMN IF EXISTS search_vector, DROP COLUMN IF EXISTS importance,
    DROP COLUMN IF EXISTS confidence,  DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS project_id;
```
App rollback = revert the Go struct/repo/test changes. Estimated PR diff: ~120 changed lines — well under the 400-line review budget.

## Open Questions

- [ ] Reject negative `confidence` at the app layer, or trust the DB? (`confidence` has no CHECK; spec doesn't say.)
- [ ] Add a `project_id` index now, or wait until the `projects` table is defined? (No FK in this change; queries by `project_id` would full-scan.)
- [ ] Confirm with team that **narrowing** (not deleting) "Exclude Retrieval and Channel Behavior" is the intended archive-time action.
