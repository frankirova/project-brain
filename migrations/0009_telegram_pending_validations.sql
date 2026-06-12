-- Migration 0009: durable storage for Telegram pending human validations.
--
-- The collision-validation flow generates a short token, sends an inline
-- keyboard with two buttons (keep / discard), and waits for the human to
-- tap one. Until now the (token → request) mapping lived in process
-- memory, so a restart invalidated every outstanding button with "no
-- longer available" — annoying but harmless, since the source message
-- was untouched. This table makes the mapping survive restarts.
--
-- Take is a hard DELETE (the old map did this too): a button can be
-- acted on at most once. expires_at is the TTL cutoff: Take only
-- returns rows whose expires_at is NULL or still in the future, and
-- SweepExpired reaps the rest on startup so the table cannot grow
-- without bound across restarts.

CREATE TABLE IF NOT EXISTS telegram_pending_validations (
    token TEXT PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    request JSONB NOT NULL,
    collision JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_telegram_pending_validations_created_at
    ON telegram_pending_validations (created_at);

-- Backs the SweepExpired startup GC pass: a partial-friendly range
-- scan over the cutoff column is cheap even with many stale rows.
CREATE INDEX IF NOT EXISTS idx_telegram_pending_validations_expires_at
    ON telegram_pending_validations (expires_at)
    WHERE expires_at IS NOT NULL;
