-- Migration 0006: FTS coverage for tags and weight balance.
--
-- The search_vector generated column originally indexed only
-- title + summary + content. Tags are first-class metadata
-- that users will want to search. The new definition also
-- weights matches in title higher than content, which makes
-- relevance ranking more useful once Fase 2 adds retrieval.

DROP INDEX IF EXISTS knowledge_objects_search_vector_idx;
ALTER TABLE knowledge_objects DROP COLUMN IF EXISTS search_vector;
ALTER TABLE knowledge_objects ADD COLUMN search_vector tsvector
  GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('simple', coalesce(summary, '')), 'B') ||
    setweight(to_tsvector('simple', coalesce(content, '')), 'C') ||
    setweight(to_tsvector('simple', coalesce(array_to_string(tags, ' '), '')), 'B')
  ) STORED;
CREATE INDEX knowledge_objects_search_vector_idx
  ON knowledge_objects USING GIN (search_vector);
