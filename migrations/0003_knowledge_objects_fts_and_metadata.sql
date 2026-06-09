-- 0003_knowledge_objects_fts_and_metadata.sql
-- Additive: extends knowledge_objects with §10.1 metadata columns and a
-- full-text search tsvector + GIN index. Idempotent so re-running is a no-op.
--
-- All new metadata columns are NULLable except tags, which has a safe
-- default. The FTS column is a STORED generated column maintained by
-- Postgres, so the 4-write application contract per ingest is preserved.

ALTER TABLE knowledge_objects
    ADD COLUMN IF NOT EXISTS project_id    UUID,
    ADD COLUMN IF NOT EXISTS tags          TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS confidence    DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS importance    INTEGER
        CHECK (importance IS NULL OR (importance BETWEEN 0 AND 100)),
    ADD COLUMN IF NOT EXISTS search_vector tsvector
        GENERATED ALWAYS AS (to_tsvector('simple',
            coalesce(title,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(content,'')
        )) STORED;

CREATE INDEX IF NOT EXISTS knowledge_objects_search_vector_idx
    ON knowledge_objects USING GIN (search_vector);
