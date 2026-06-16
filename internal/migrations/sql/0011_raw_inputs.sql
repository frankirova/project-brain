-- Migration 0011: staging table for raw inputs before ingestion.
--
-- raw_inputs captures every message that enters the system before any
-- collision detection or ingestion decision is made. The lifecycle is
-- a three-state machine: pending (created on message receipt) ->
-- promoted (ingested as a knowledge_object) | discarded (user chose to
-- discard after collision). promoted_object_id is set when promoted and
-- NULL otherwise; collision_summary holds the top collision entry when
-- the message was flagged before the user decided.
--
-- The table is standalone: it is not part of the 4-write ingest
-- transaction. Promotion and discard updates are best-effort (logged,
-- never block the user reply), mirroring the EmbeddingHook pattern.
--
-- telegram_pending_validations gains a nullable raw_input_id so the
-- callback handler can link a pending validation back to its raw_input
-- row without a foreign key constraint (which would create an
-- insert-ordering dependency). NULL means the row was written before
-- this migration (forward-compat).

CREATE TABLE IF NOT EXISTS raw_inputs (
    id                 UUID PRIMARY KEY,
    workspace_id       TEXT NOT NULL,
    channel            TEXT NOT NULL,
    content            TEXT NOT NULL,
    actor_id           TEXT,
    external_ref       JSONB NOT NULL DEFAULT '{}',
    status             TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'promoted', 'discarded')),
    promoted_object_id UUID REFERENCES knowledge_objects(id) ON DELETE SET NULL,
    collision_summary  JSONB,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_raw_inputs_workspace_status
    ON raw_inputs (workspace_id, status);

CREATE INDEX IF NOT EXISTS idx_raw_inputs_workspace_created_at
    ON raw_inputs (workspace_id, created_at DESC);

ALTER TABLE telegram_pending_validations
    ADD COLUMN IF NOT EXISTS raw_input_id UUID;
