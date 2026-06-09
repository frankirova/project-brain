# Exploration: Knowledge Pipeline

## Current State

`IngestTextService.Ingest` is purely synchronous and dumb-pipe: validate → check identity-key duplicate → create 4 records (Source, KnowledgeObject, ObjectSource, AuditEvent) in one transaction. Every field the caller sends (title, summary, type, status, metadata) is persisted verbatim. The 12 middle steps of PROJECT_BRAIN.md §13 (normalize-through-relations) are completely absent. The DB runs on `pgvector/pgvector:pg16` (extension available, unused). `knowledge_objects` has no FTS column, no embedding column, no `project_id`, no `tags`, no `confidence`, no `importance`. Test `TestIngestDoesNotRequireDeferredExternalCapabilities` explicitly locks the contract at exactly 4 writes per ingest.

## Affected Areas

- `internal/app/ingest_text.go` — pipeline entry point; `prepareIngestText` already does the Normalize step.
- `internal/app/ports.go` — `IngestionRepositories` will need new methods (or new ports for embedder/classifier later).
- `internal/postgres/repositories.go` — `knowledgeObjectRepository.Create` is the only writer; needs read/update for FTS, project_id, tags, confidence, importance.
- `internal/postgres/db.go` — `WithinIngestionTx` must not be extended to do slow work (no embedding calls inside the tx).
- `internal/domain/knowledge.go` — `KnowledgeObject` is missing `ProjectID`, `Tags`, `Confidence`, `Importance` per PROJECT_BRAIN.md §10.1.
- `migrations/0001_knowledge_core_ingestion.sql` — add `project_id`, `tags`, `confidence`, `importance`, generated `tsvector` column, GIN index.
- `migrations/0002_relations.sql` — schema already correct; no change.
- `internal/httpapi/handler.go`, `internal/telegram/handler.go` — call sites; contract must not break.
- New: `migrations/0003_embeddings.sql` (deferred to a separate change).
- Tests: `internal/app/ingest_text_test.go` and `internal/postgres/ingestion_integration_test.go` must keep passing.

## Approaches

1. **Synchronous + FTS, no LLM** — add FTS column+index, expand `knowledge_objects` to match §10.1 (nullable fields), no embeddings/classify/tags/relations. Keep `WithinIngestionTx` unchanged.
   - Pros: No external deps, no async, no API keys, fits existing 4-write contract (generated column, no extra write), keeps PR small, test stays green.
   - Cons: Embeddings/auto-tagging/relations deferred to follow-up changes.
   - Effort: **Low**.

2. **Synchronous + FTS, async embeddings** — same as #1 plus a background worker that reads new rows and generates embeddings.
   - Pros: Unlocks hybrid RAG; ingestion latency stays low.
   - Cons: New worker process, queue plumbing, retry/backfill logic.
   - Effort: **Medium**.

3. **Full async event-driven pipeline (NATS + per-step workers)** — matches §5 Architecture E.
   - Pros: Long-term correct, reprocessable.
   - Cons: Massive scope, infra-heavy, §16 phases this for Phase 5+.
   - Effort: **High**.

4. **Synchronous + local ONNX embedder** — local `all-MiniLM-L6-v2` model, no external API.
   - Pros: No API key, offline-capable.
   - Cons: ONNX runtime is a heavy C++ dep, model file management, slower per-request.
   - Effort: **Medium-High**.

## Recommendation

**Approach A** for this change. Stay disciplined: FTS column + GIN index + expand `knowledge_objects` schema to match §10.1 (additive, nullable). Do NOT add embeddings, classification, summarization, auto-tagging, related-memory search, relation creation, or chunking here — each of those is its own follow-up change. The existing `TestIngestDoesNotRequireDeferredExternalCapabilities` test is a feature, not a bug; it codifies the MVP contract. Use a `GENERATED ALWAYS AS ... STORED` `tsvector` column so the FTS index stays in sync with zero application code and zero extra writes. Use `'simple'` config for the tsvector (project is bilingual ES/EN; `'simple'` is neutral, language-aware query config can come later).

Defer to a separate `embeddings-pgvector` change (Approach B or D) — don't conflate FTS work with embedding plumbing.

## Risks

- **FTS language config**: `'spanish'` would mis-stem English content; `'simple'` is the safe MVP default.
- **GIN write cost**: acceptable at MVP volumes; document the tradeoff.
- **Generated column dependency**: PostgreSQL 12+ supports `STORED` generated columns; `pg16` does, no issue.
- **400-line PR budget**: must stay under it — FTS + schema only, no embedder ports yet.
- **pgvector extension**: not needed for THIS change, but the next change must `CREATE EXTENSION IF NOT EXISTS vector;` before the embeddings migration runs.
- **Test breakage**: any new write inside the tx would break the "4 writes" assertion — the recommended approach uses a generated column precisely to avoid this.

## Ready for Proposal

**Yes**. Scope is well-bounded, additive, the synchronous ingestion contract is preserved, and it unlocks Phase 3 (FTS-based keyword search) from the §16 roadmap without painting us into a corner. Next: `sdd-propose` for `knowledge-pipeline` with FTS + §10.1 schema alignment; follow-up changes for embeddings, auto-classification, and relations.
