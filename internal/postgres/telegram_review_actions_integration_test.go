package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/frankirova/project-brain/internal/telegram"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sampleTelegramReviewAction() app.TelegramReviewAction {
	return app.TelegramReviewAction{
		Token:          uuid.NewString(),
		WorkspaceID:    "default",
		ActorID:        12345,
		ChatID:         4242,
		ObjectID:       uuid.New(),
		Action:         telegram.TelegramReviewActionValidate,
		ExpectedStatus: domain.KnowledgeObjectStatusProposed,
		NextCursor:     "next-page-token",
		CreatedAt:      time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC),
		ExpiresAt:      time.Now().Add(telegram.TelegramReviewActionTTL),
	}
}

func TestTelegramReviewActionStoreSaveTakeAndSweep(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewTelegramReviewActionStore(db.pool)
	ctx := context.Background()

	action := sampleTelegramReviewAction()
	if err := store.Save(ctx, action); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Cleanup(func() { cleanupTelegramReviewAction(t, db.pool, action.Token) })

	got, err := store.Take(ctx, action.Token)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got.Token != action.Token || got.WorkspaceID != action.WorkspaceID || got.ObjectID != action.ObjectID {
		t.Fatalf("round-trip mismatch: got %#v, want %#v", got, action)
	}
	if got.ActorID != action.ActorID || got.ChatID != action.ChatID || got.Action != action.Action || got.ExpectedStatus != action.ExpectedStatus {
		t.Fatalf("context mismatch: got %#v, want %#v", got, action)
	}
	if _, err := store.Take(ctx, action.Token); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("second Take err = %v, want app.ErrNotFound", err)
	}
	if _, err := store.Take(ctx, "never-saved-"+uuid.NewString()); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("unknown Take err = %v, want app.ErrNotFound", err)
	}

	expired := sampleTelegramReviewAction()
	expired.Token = "expired-" + uuid.NewString()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	active := sampleTelegramReviewAction()
	active.Token = "active-" + uuid.NewString()
	active.ExpiresAt = time.Now().Add(time.Hour)
	for _, a := range []app.TelegramReviewAction{expired, active} {
		if err := store.Save(ctx, a); err != nil {
			t.Fatalf("Save %s: %v", a.Token, err)
		}
		t.Cleanup(func() { cleanupTelegramReviewAction(t, db.pool, a.Token) })
	}
	if _, err := store.Take(ctx, expired.Token); !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("expired Take err = %v, want app.ErrNotFound", err)
	}

	expired.Token = "sweep-" + uuid.NewString()
	if err := store.Save(ctx, expired); err != nil {
		t.Fatalf("Save sweep candidate: %v", err)
	}
	removed, err := store.SweepExpired(ctx)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if removed < 1 {
		t.Fatalf("removed = %d, want at least 1", removed)
	}
	if _, err := store.Take(ctx, active.Token); err != nil {
		t.Fatalf("active token after sweep: %v", err)
	}
}

func cleanupTelegramReviewAction(t *testing.T, pool *pgxpool.Pool, token string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "DELETE FROM telegram_pending_review_actions WHERE token = $1", token); err != nil {
		t.Fatalf("cleanup Telegram review action %s: %v", token, err)
	}
}
