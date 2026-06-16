-- Migration 0013: durable storage for Telegram backlog review-action tokens.
--
-- Review-action callbacks use Telegram payloads like `rv:<token>`. The payload
-- is intentionally opaque and short; trusted context lives server-side here so
-- future Telegram callback handling can verify workspace, actor, chat, target
-- object, expected status, action, and expiry before calling app services.
--
-- This table is separate from telegram_pending_validations. Collision
-- validations store ingest requests and collision JSON; backlog review actions
-- store lifecycle intent for existing knowledge objects. Mixing them would make
-- both callback meanings nullable and easy to misuse.

CREATE TABLE IF NOT EXISTS telegram_pending_review_actions (
    token TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    actor_id BIGINT NOT NULL,
    chat_id BIGINT NOT NULL,
    object_id UUID NOT NULL,
    action TEXT NOT NULL,
    expected_status TEXT NOT NULL,
    next_cursor TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_telegram_pending_review_actions_workspace_actor
    ON telegram_pending_review_actions (workspace_id, actor_id);

-- Backs the startup GC pass and keeps stale inline buttons from leaving rows
-- behind indefinitely.
CREATE INDEX IF NOT EXISTS idx_telegram_pending_review_actions_expires_at
    ON telegram_pending_review_actions (expires_at)
    WHERE expires_at IS NOT NULL;
