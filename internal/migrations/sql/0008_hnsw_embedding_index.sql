-- Migration 0008: HNSW index on embeddings.embedding for fast
-- approximate nearest-neighbour search by cosine distance.
--
-- Migration 0007's comment claimed an HNSW index but only created a
-- B-tree on workspace_id, so vector search was a sequential scan. This
-- adds the real index. The vector_cosine_ops opclass matches the `<=>`
-- (cosine distance) operator used by EmbeddingRepo.FindSimilar.
--
-- NOTE: at production scale, build this CONCURRENTLY to avoid blocking
-- writes. CREATE INDEX CONCURRENTLY cannot run inside a transaction
-- block, so it is omitted here for the simple file-applied migration
-- path; the corpus is small enough at MVP scale that a brief lock is
-- acceptable.

CREATE INDEX IF NOT EXISTS idx_embeddings_hnsw_cosine
  ON embeddings USING hnsw (embedding vector_cosine_ops);
