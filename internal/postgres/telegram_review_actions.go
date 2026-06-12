package postgres

import (
	"context"
	"errors"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TelegramReviewActionStore is the PostgreSQL implementation of
// app.TelegramReviewActionStore. It persists only trusted callback
// context for Telegram backlog review buttons; it does not execute or
// interpret lifecycle policy.
type TelegramReviewActionStore struct {
	pool *pgxpool.Pool
}

// NewTelegramReviewActionStore returns a review-action store backed by
// the given pool. Migrations create the table; the store does not run
// migrations itself.
func NewTelegramReviewActionStore(pool *pgxpool.Pool) *TelegramReviewActionStore {
	return &TelegramReviewActionStore{pool: pool}
}

// Save upserts action by token. CreatedAt is persisted from the caller
// when supplied; a zero CreatedAt falls back to now() so older callers
// cannot create invalid rows. A zero ExpiresAt is stored as SQL NULL and
// therefore never expires.
func (s *TelegramReviewActionStore) Save(ctx context.Context, action app.TelegramReviewAction) error {
	var createdAt any
	if !action.CreatedAt.IsZero() {
		createdAt = action.CreatedAt
	}
	var expiresAt any
	if !action.ExpiresAt.IsZero() {
		expiresAt = action.ExpiresAt
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO telegram_pending_review_actions (
  token, workspace_id, actor_id, chat_id, object_id, action, expected_status, next_cursor, created_at, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9::timestamptz, now()), $10)
ON CONFLICT (token) DO UPDATE
  SET workspace_id = EXCLUDED.workspace_id,
      actor_id = EXCLUDED.actor_id,
      chat_id = EXCLUDED.chat_id,
      object_id = EXCLUDED.object_id,
      action = EXCLUDED.action,
      expected_status = EXCLUDED.expected_status,
      next_cursor = EXCLUDED.next_cursor,
      created_at = EXCLUDED.created_at,
      expires_at = EXCLUDED.expires_at`,
		action.Token,
		action.WorkspaceID,
		action.ActorID,
		action.ChatID,
		action.ObjectID,
		action.Action,
		action.ExpectedStatus,
		action.NextCursor,
		createdAt,
		expiresAt,
	)
	return err
}

// Take loads and deletes the action for token. DELETE ... RETURNING is
// the atomic single-use boundary: one racing callback can win, and all
// others observe app.ErrNotFound. Expired rows also return ErrNotFound.
func (s *TelegramReviewActionStore) Take(ctx context.Context, token string) (app.TelegramReviewAction, error) {
	var action app.TelegramReviewAction
	var expiresAt pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, `
DELETE FROM telegram_pending_review_actions
WHERE token = $1
  AND (expires_at IS NULL OR expires_at > now())
RETURNING token, workspace_id, actor_id, chat_id, object_id, action, expected_status, next_cursor, created_at, expires_at`,
		token,
	).Scan(
		&action.Token,
		&action.WorkspaceID,
		&action.ActorID,
		&action.ChatID,
		&action.ObjectID,
		&action.Action,
		&action.ExpectedStatus,
		&action.NextCursor,
		&action.CreatedAt,
		&expiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return app.TelegramReviewAction{}, app.ErrNotFound
		}
		return app.TelegramReviewAction{}, err
	}
	if expiresAt.Valid {
		action.ExpiresAt = expiresAt.Time
	}
	return action, nil
}

// SweepExpired deletes expired review-action rows and returns the number
// removed. Rows with NULL expires_at are intentionally retained.
func (s *TelegramReviewActionStore) SweepExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
DELETE FROM telegram_pending_review_actions
WHERE expires_at IS NOT NULL AND expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

var _ app.TelegramReviewActionStore = (*TelegramReviewActionStore)(nil)
