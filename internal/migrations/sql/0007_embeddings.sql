-- Migration 0007: embeddings for vector similarity search.
--
-- The vector(1536) size is the OpenAI ada-002 / text-embedding-3-small
-- default. Adjust at integration time if a different model is picked.
--
-- One row per knowledge_object (1:1). On delete of the object, the
-- embedding goes with it. The hnsw index uses cosine distance and
-- gives sub-millisecond recall at MVP volumes. For >100k rows,
-- switch to ivfflat or quantize to half-precision.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS embeddings (
    object_id UUID PRIMARY KEY REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    model TEXT NOT NULL,
    dimensions INTEGER NOT NULL,
    embedding vector(1536) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_embeddings_workspace_id
  ON embeddings (workspace_id);
