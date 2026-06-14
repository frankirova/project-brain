-- 0015_knowledge_objects_confidence_check_and_project_id_index.sql
-- Additive: enforces confidence ∈ [0, 1] on knowledge_objects and
-- indexes project_id for future filter / join performance.
-- Idempotent so re-running is a no-op.
--
-- Mirrors the existing confidence CHECK on the relations table
-- (migrations/0002_relations.sql:7) so the two tables behave
-- consistently. The app-level guard in
-- internal/app/ingest_text.go is the primary defense; the DB
-- CHECK is the safety net for direct SQL writes.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'knowledge_objects_confidence_range'
          AND conrelid = 'knowledge_objects'::regclass
    ) THEN
        ALTER TABLE knowledge_objects
            ADD CONSTRAINT knowledge_objects_confidence_range
            CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1));
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS knowledge_objects_project_id_idx
    ON knowledge_objects USING btree (project_id);
