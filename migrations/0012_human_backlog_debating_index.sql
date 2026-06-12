-- Migration 0012: partial index for the human backlog query.
--
-- ObjectDebateService.ListHumanBacklog (change 14, PR 3) orders
-- the response by (updated_at DESC, id DESC) and filters to
-- status = 'proposed' | 'debating' | recent-'deprecated' within a
-- workspace. Of those three, only the 'debating' portion is expected
-- to grow large: 'proposed' is short-lived (humans act on it quickly)
-- and recent-'deprecated' has a 14-day recency window enforced at
-- read time.
--
-- This partial index covers the 'debating' subset with a
-- workspace-scoped composite (workspace_id, updated_at DESC, id DESC).
-- The 'proposed' and recent-'deprecated' portions fall back to a
-- full scan; in practice both are tiny per workspace and the
-- planner degrades gracefully.
--
-- IF NOT EXISTS makes the migration safe to re-run. There is no
-- schema change to knowledge_objects or audit_events; this is the
-- only artifact in the migration.

CREATE INDEX IF NOT EXISTS idx_knowledge_objects_debating
  ON knowledge_objects (workspace_id, updated_at DESC, id DESC)
  WHERE status = 'debating';
