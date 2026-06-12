package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PendingValidationStore is the PostgreSQL implementation of
// app.PendingValidationStore. It serializes the full IngestTextRequest
// and Collision as JSONB so the row is the single source of truth —
// the Telegram handler can reconstruct a callback action after a
// restart without any side tables.
//
// Take uses DELETE ... RETURNING to give the load-and-delete
// semantics the contract requires without a transaction: if two
// callbacks race for the same token (double-tap), exactly one of them
// gets a row back, the other gets pgx.ErrNoRows → app.ErrNotFound.
type PendingValidationStore struct {
	pool *pgxpool.Pool
}

// NewPendingValidationStore returns a store backed by the given pool.
// The migrations package creates the table on first boot; this type
// does not run migrations itself.
func NewPendingValidationStore(pool *pgxpool.Pool) *PendingValidationStore {
	return &PendingValidationStore{pool: pool}
}

// Save upserts an entry by token. Overwrite is total so a buggy
// caller cannot deadlock on a primary-key conflict, and the Telegram
// handler generates a fresh UUID per prompt so collisions are not
// expected in practice. The ExpiresAt cutoff is persisted as a
// TIMESTAMPTZ so the TTL filter on Take and the SweepExpired GC pass
// can both run against the same source of truth; a zero ExpiresAt is
// stored as SQL NULL so the row is treated as "never expires" by both
// paths.
func (s *PendingValidationStore) Save(ctx context.Context, entry app.PendingValidation) error {
	request, err := json.Marshal(entry.Request)
	if err != nil {
		return err
	}
	collision, err := json.Marshal(entry.Collision)
	if err != nil {
		return err
	}
	var expiresAt any
	if !entry.ExpiresAt.IsZero() {
		expiresAt = entry.ExpiresAt
	}
	_, err = s.pool.Exec(ctx, `
INSERT INTO telegram_pending_validations (token, chat_id, request, collision, expires_at)
VALUES ($1, $2, $3::jsonb, $4::jsonb, $5)
ON CONFLICT (token) DO UPDATE
  SET chat_id = EXCLUDED.chat_id,
      request = EXCLUDED.request,
      collision = EXCLUDED.collision,
      created_at = now(),
      expires_at = EXCLUDED.expires_at`,
		entry.Token, entry.ChatID, request, collision, expiresAt,
	)
	return err
}

// Take loads and deletes the entry for token, returning
// app.ErrNotFound when the token is unknown, has already been
// consumed, OR has expired (TTL enforcement — a stale prompt must not
// resurrect a forgotten input). On a successful Take the row is gone —
// same contract as the in-memory map, just durable. Rows with a NULL
// expires_at have no TTL and are always eligible.
func (s *PendingValidationStore) Take(ctx context.Context, token string) (app.PendingValidation, error) {
	var (
		chatID int64
		rawReq []byte
		rawCol []byte
	)
	err := s.pool.QueryRow(ctx, `
DELETE FROM telegram_pending_validations
WHERE token = $1
  AND (expires_at IS NULL OR expires_at > now())
RETURNING chat_id, request, collision`,
		token,
	).Scan(&chatID, &rawReq, &rawCol)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return app.PendingValidation{}, app.ErrNotFound
		}
		return app.PendingValidation{}, err
	}

	var req domain.IngestTextRequest
	if err := json.Unmarshal(rawReq, &req); err != nil {
		return app.PendingValidation{}, err
	}
	var col app.Collision
	if err := json.Unmarshal(rawCol, &col); err != nil {
		return app.PendingValidation{}, err
	}
	return app.PendingValidation{
		Token:     token,
		ChatID:    chatID,
		Request:   req,
		Collision: col,
	}, nil
}

// SweepExpired deletes every row whose expires_at has already passed
// and returns the number of rows reaped. It is a one-shot GC pass
// designed to be called from main.go on startup (and is therefore
// intentionally idempotent and side-effect free beyond the DELETE).
// The composition root only calls this when a PostgreSQL backend is
// wired, so the method lives on the concrete type and does not need
// to be part of the in-memory contract.
func (s *PendingValidationStore) SweepExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
DELETE FROM telegram_pending_validations
WHERE expires_at IS NOT NULL AND expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

var _ app.PendingValidationStore = (*PendingValidationStore)(nil)
