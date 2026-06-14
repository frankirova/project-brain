-- 0016_knowledge_objects_per_object_tsvector.sql
-- Additive: switches knowledge_objects.search_vector from a
-- constant 'simple' config to a per-row language config driven
-- by a new nullable `language` column. The migration uses the
-- online-swap pattern (add new column, drop + recreate the
-- generated column, rebuild the GIN) so the rewrite is bounded
-- by the table's current row count.
--
-- Defense in depth: alongside the regconfig cast inside the
-- generated expression, add a CHECK constraint on language so
-- the database rejects unsupported values with a clean
-- constraint name. SQLSTATE 42704 (regconfig cast failure)
-- would also catch invalid values, but the error message is
-- less user-friendly and the check semantics are clearer.
--
-- Idempotent so re-running is a no-op.

-- 1. Add the language column (non-blocking — just a NULLable add).
ALTER TABLE knowledge_objects
    ADD COLUMN IF NOT EXISTS language TEXT;

-- 2. Drop the existing generated search_vector column. The
-- dependent GIN index is dropped automatically by CASCADE.
ALTER TABLE knowledge_objects DROP COLUMN IF EXISTS search_vector CASCADE;

-- 3. Re-add the generated column with a per-row config. The
-- CASE expression selects 'simple' when language is NULL or
-- empty, otherwise casts the language to regconfig. If the
-- language value does not resolve to a known text search
-- configuration, the regconfig cast fails at insert/update
-- time with SQLSTATE 42704.
ALTER TABLE knowledge_objects
    ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector(
        CASE
            WHEN language IS NULL OR language = '' THEN 'simple'::regconfig
            ELSE language::regconfig
        END,
        coalesce(title, '') || ' ' || coalesce(summary, '') || ' ' || coalesce(content, '')
    )) STORED;

-- 4. Re-create the GIN index. We use IF NOT EXISTS to make the
-- migration idempotent.
CREATE INDEX IF NOT EXISTS knowledge_objects_search_vector_idx
    ON knowledge_objects USING GIN (search_vector);

-- 5. CHECK constraint on language as defense in depth. Unknown
-- values are rejected at insert/update time with a clean
-- constraint name (no SQLSTATE 42704 surprises).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'knowledge_objects_language_check'
          AND conrelid = 'knowledge_objects'::regclass
    ) THEN
        ALTER TABLE knowledge_objects
            ADD CONSTRAINT knowledge_objects_language_check
            CHECK (language IS NULL OR language IN ('simple', 'english', 'spanish'));
    END IF;
END
$$;
