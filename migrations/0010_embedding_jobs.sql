-- Migration 0010: durable retry queue for failed embedding generation.
--
-- The post-ingest embedding hook is best-effort by contract: a Gemini
-- outage degrades search recall but never blocks ingestion. Before this
-- migration that failure was logged and forgotten, so the object stayed
-- vector-less forever — a silent recall hole. This table makes the
-- failure recoverable: the hook enqueues on error, a worker picks due
-- rows on an interval, a successful retry deletes the row.
--
-- Composite PK (object_id, model) gives free dedup when the same object
-- is enqueued twice (e.g. a duplicate ingest path retrying the hook)
-- while still letting a future model change coexist with the old
-- model's pending retries.
--
-- ON DELETE CASCADE on object_id means a deleted knowledge_object takes
-- its pending retries with it — no orphan rows.
--
-- ClaimDue uses FOR UPDATE SKIP LOCKED and bumps next_retry_at to a
-- short lease window so the queue is safe for multiple workers without
-- a separate locked_at column.

CREATE TABLE IF NOT EXISTS embedding_jobs (
    object_id UUID NOT NULL REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (object_id, model)
);

-- Backs the ClaimDue scan: a range read over due rows ordered by
-- next_retry_at stays cheap even with a backlog of paused jobs.
CREATE INDEX IF NOT EXISTS idx_embedding_jobs_next_retry_at
    ON embedding_jobs (next_retry_at);
