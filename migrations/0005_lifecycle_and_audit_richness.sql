-- Migration 0005: knowledge object lifecycle + audit event richness.
-- Forward-only changes:
--   1. knowledge_objects.status: add CHECK constraint enforcing the
--      lifecycle values (active, proposed, debating, validated, deprecated).
--   2. audit_events.before: nullable JSONB snapshot of the prior state.
--   3. audit_events.reason: nullable TEXT for human-readable rationale.
--   4. audit_events.request_id: nullable UUID to correlate multiple events
--      from a single user action across services.
--   5. audit_events.target_id: drop the FK to knowledge_objects so the
--      column is polymorphic (can reference relations, raw_inputs, etc.
--      in future changes without further migrations).
--   6. audit_events.metadata: JSONB for structured extra context
--      (separate from before/after diffs).

-- Existing rows have status = 'active' (the historical default). The
-- new CHECK allows 'active' plus four new lifecycle values.

ALTER TABLE knowledge_objects
    DROP CONSTRAINT IF EXISTS knowledge_objects_status_check;
ALTER TABLE knowledge_objects
    ADD CONSTRAINT knowledge_objects_status_check
    CHECK (status IN ('active', 'proposed', 'debating', 'validated', 'deprecated'));

ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS before JSONB;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS reason TEXT;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS request_id UUID;
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}';

-- Drop the FK to knowledge_objects. The target_type + target_id pair is
-- now polymorphic; the application layer is responsible for integrity.
ALTER TABLE audit_events DROP CONSTRAINT IF EXISTS audit_events_target_id_fkey;
