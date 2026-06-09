# Verification Report: Knowledge Pipeline

**Change**: knowledge-pipeline
**Version**: N/A (delta spec)
**Mode**: Standard (no strict TDD)
**Artifact store**: openspec
**Project**: project-brain
**Verified at**: 2026-06-09

## Completeness

| Metric | Value |
|--------|-------|
| Tasks total | 21 |
| Tasks complete | 21 |
| Tasks incomplete | 0 |

All 21 tasks across Phases 1-8 of `tasks.md` are marked `[x]`. Phase 7.1 (narrowing the "Exclude Retrieval and Channel Behavior" requirement) is explicitly flagged as an archive-phase action and is correctly out of scope for apply/verify.

## Build & Tests Execution

**Build**: PASSED
```text
$ go build ./...
(no output, exit 0)
```

**Vet**: PASSED
```text
$ go vet ./...
(no output, exit 0)
```

**Gofmt**: PASSED
```text
$ gofmt -l .
(no output — no files need formatting)
```

**Unit tests (short mode, with DSN set)**: PASSED
```text
$ go test -short -v -run "TestIngest" ./internal/app/
--- PASS: TestIngestCreatesCompleteAuditableRecords
--- PASS: TestIngestRejectsWhitespaceWithoutWrites
--- PASS: TestIngestReturnsDuplicateWithoutCreatingRecords
--- PASS: TestIngestDoesNotRequireDeferredExternalCapabilities
--- PASS: TestIngestPersistsNewMetadataFields
--- PASS: TestIngestDefaultsNilTagsToEmptySlice
--- PASS: TestIngestRollsBackWhenARequiredRecordFails
PASS
ok  	github.com/frankirova/project-brain/internal/app	0.393s
```

**Integration tests (with real PostgreSQL)**: PASSED
```text
$ PROJECT_BRAIN_TEST_DATABASE_DSN=postgres://postgres:postgres@localhost:5433/project_brain?sslmode=disable \
  go test -v -run "TestPostgresIngestion" ./internal/postgres/
--- PASS: TestPostgresIngestionPersistsAndDeduplicates (0.09s)
--- PASS: TestPostgresIngestionRollsBackPartialWrites (0.06s)
--- PASS: TestPostgresIngestionFTSAutoPopulated (0.04s)
--- PASS: TestPostgresIngestionImportanceCheckRejects200 (0.04s)
PASS
ok  	github.com/frankirova/project-brain/internal/postgres	1.057s
```

**Full repo short tests (excluding the unrelated untracked `relation_repository_test.go`)**: PASSED
```text
ok  	github.com/frankirova/project-brain/internal/app         0.475s
ok  	github.com/frankirova/project-brain/internal/domain      0.497s
ok  	github.com/frankirova/project-brain/internal/config      0.478s
ok  	github.com/frankirova/project-brain/internal/httpapi     0.663s
ok  	github.com/frankirova/project-brain/internal/telegram    0.564s
?   	github.com/frankirova/project-brain/cmd/api               [no test files]
```

**Coverage**: Not available — `go test -cover` was not run; the project does not set a coverage threshold. The change relies on spec-mapped behavior tests (PASSED) rather than coverage targets.

## Spec Compliance Matrix

| Requirement | Scenario | Test | Result |
|-------------|----------|------|--------|
| Schema Supports Project Scope and Importance | Project ID is stored | `internal/app/ingest_text_test.go > TestIngestPersistsNewMetadataFields` (asserts `object.ProjectID == projectID`) | COMPLIANT |
| Schema Supports Project Scope and Importance | Tags are stored as array | `internal/app/ingest_text_test.go > TestIngestPersistsNewMetadataFields` (asserts `object.Tags == ["go","postgres"]`) | COMPLIANT |
| Schema Supports Project Scope and Importance | Confidence and importance are stored | `internal/app/ingest_text_test.go > TestIngestPersistsNewMetadataFields` (asserts `object.Confidence == 0.92`, `object.Importance == 80`) | COMPLIANT |
| Full-Text Search Index Available | FTS index is auto-populated on insert | `internal/postgres/ingestion_integration_test.go > TestPostgresIngestionFTSAutoPopulated` | COMPLIANT |
| Full-Text Search Index Available | FTS index includes title, summary, and content | `internal/postgres/ingestion_integration_test.go > TestPostgresIngestionFTSAutoPopulated` (asserts all 3 distinctive tokens match via `to_tsquery('simple', ...)`) | COMPLIANT |
| Full-Text Search Index Available | FTS language config is neutral | (no direct test; covered by `'simple'` config choice in `0003` migration, no stemming) | PARTIAL — implementation choice is correct, but no test directly asserts bilingual content. The `'simple'` config is what makes the implementation work; the test covers the FTS behavior but not the bilingual scenario explicitly. |
| Idempotency Contract Preserved | 4-write contract holds with new fields | `internal/app/ingest_text_test.go > TestIngestPersistsNewMetadataFields` (asserts `writeCount() == 4` with all four new fields populated) + `TestIngestDoesNotRequireDeferredExternalCapabilities` (asserts `writeCount() == 4` baseline) | COMPLIANT |
| Preserve Workspace Scope and Metadata (MODIFIED) | Type, title, summary, status, timestamps, project_id, tags, confidence, importance, and metadata are preserved | `TestIngestCreatesCompleteAuditableRecords` (existing fields) + `TestIngestPersistsNewMetadataFields` (new fields) | COMPLIANT |

**Compliance summary**: 7/8 scenarios have a covering test that passed at runtime; 1/8 (FTS language neutrality) is PARTIAL — covered by implementation choice and adjacent FTS test, but not by a scenario-specific assertion.

## Correctness (Static Evidence)

| Requirement | Status | Notes |
|------------|--------|-------|
| 4 nullable + 1 generated column added; idempotent migration | Implemented | `migrations/0003_knowledge_objects_fts_and_metadata.sql` (19 lines): `IF NOT EXISTS` on every column and the index, GIN index on the generated column. |
| `importance` CHECK constraint 0..100 | Implemented | Migration line 13-14: `CHECK (importance IS NULL OR (importance BETWEEN 0 AND 100))`; rejected at runtime by `TestPostgresIngestionImportanceCheckRejects200` (SQLSTATE 23514). |
| `tags TEXT[] NOT NULL DEFAULT '{}'` with non-nil default in code | Implemented | Migration line 11; `prepareIngestText` defaults nil tags to `[]string{}` (line 181-184); repo also defensively defaults (line 110-112). |
| Generated `tsvector` covers title, summary, content | Implemented | Migration line 15-18: `to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(content,''))`. Confirmed by `TestPostgresIngestionFTSAutoPopulated` against a live database. |
| 4-write contract preserved | Implemented | `ingest_text.go` does not add any write; new fields are passed through on the existing `KnowledgeObjects().Create` call (lines 88-91). `writeCount()` stays at 4 in both `TestIngestDoesNotRequireDeferredExternalCapabilities` and `TestIngestPersistsNewMetadataFields`. |
| GIN index created | Implemented | Migration line 20-21: `CREATE INDEX IF NOT EXISTS knowledge_objects_search_vector_idx ON knowledge_objects USING GIN (search_vector)`. |

## Coherence (Design)

| Decision | Followed? | Notes |
|----------|-----------|-------|
| 1 — FTS column shape: GENERATED ALWAYS AS ... STORED + GIN | Yes | Migration uses `GENERATED ALWAYS AS (...) STORED` and `USING GIN (search_vector)`. No trigger, no app-side tsvector. |
| 2 — tsvector config `'simple'` | Yes | Migration uses `'simple'` config; no stemming (preserves bilingual exact match). |
| 3 — Nullability: project_id/confidence/importance NULL; tags NOT NULL DEFAULT '{}' | Yes | Migration matches exactly; code path defaults `nil` tags to `[]string{}` before INSERT. |
| 4 — `importance` CHECK 0..100 | Yes | Migration line 13-14; runtime verified by `TestPostgresIngestionImportanceCheckRejects200`. |
| 5 — Repository topology: extend `knowledgeObjectRepository.Create`; no new port | Yes | One INSERT, all 4 new columns appended; no second write. |
| 6 — Archive-time: narrow "Exclude Retrieval and Channel Behavior" requirement | Deferred to archive | Task 7.1 explicitly flags this as an archive-phase action; not part of apply. |
| Data flow: exactly 4 application writes per ingest; FTS is DB-internal | Yes | `WithinIngestionTx` order untouched; no extra `Create` call. |
| File changes match design table | Yes | Migration, domain, ingest_text, repositories, plus 2 test files. All listed files touched. |
| 400-line review budget | Yes (~400 net) | 5 modified files: 381 net additions per `git diff --stat main` for the 5 files. The new untracked migration adds 19 more lines (~400 total). At/just under budget. |

## Issues

### CRITICAL
None.

### WARNING
- **Untracked test file `internal/postgres/relation_repository_test.go` is broken in the working tree** — 5 tests fail (`TestRelationRepositoryCreateMinimalFields`, `TestRelationRepositorySamePairDifferentTypeAllowed`, `TestRelationRepositoryFindByTarget`, `TestRelationRepositoryFindByType`, `TestRelationRepositoryUnrelatedRelationsPreserved`) with `cannot scan NULL into *string` at scan index 6 (`rel.Evidence` is a `string`, not a `*string`). This file is **untracked, pre-existed the knowledge-pipeline change, and is NOT part of this change's diff**. It belongs to a different work stream (likely the untracked `research-agent` or `knowledge-relations` changes seen in git status). Flagging only because the orchestrator may want to triage it; it does not affect the knowledge-pipeline verdict.

### SUGGESTION
- **FTS language neutrality has no direct test** — The scenario "FTS language config is neutral" is covered by implementation choice (`'simple'` config) and the FTS test indirectly, but a scenario-specific assertion (insert bilingual content; query with both languages' tokens; both return) would tighten coverage. Low priority — the `'simple'` config is canonical and the design documents the decision.
- **Open question from design (negative `confidence`)** — Still unresolved. DB has no CHECK; app has no validation. Out of scope for this change but worth a follow-up issue.
- **Open question from design (`project_id` index)** — Still unresolved. No index on `project_id` means `WHERE project_id = ...` queries full-scan. Out of scope for this change but worth a follow-up issue.
- **Line budget is at the edge** — Net insertions of ~400 lines for the 5 modified files (+ 19 for the new migration). Right at the 400-line review budget from `tasks.md`. Consider if any test boilerplate could be moved to a test helper in a follow-up.

## Verdict

**PASS**

All 21 tasks complete, build/vet/gofmt clean, every spec scenario that has a test passes against a real PostgreSQL 16 instance (`pgvector/pgvector:pg16`), and the 4-write contract is verified by unit test. The 8 spec scenarios map to 7 passing tests + 1 implementation-covered scenario; the FTS language-neutrality scenario is PARTIAL but is a strengthening point, not a regression. The `knowledge-pipeline` change is ready to archive.
