package inmem

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// sampleEntry builds a representative PendingValidation. The shape
// mirrors what the Telegram handler stores on collision: token, chat
// ID, a full IngestTextRequest, and a Collision. It exercises every
// field type the in-memory store has to round-trip.
func sampleEntry() app.PendingValidation {
	return app.PendingValidation{
		Token:  uuid.NewString(),
		ChatID: 4242,
		Request: domain.IngestTextRequest{
			WorkspaceID: "default",
			Content:     "propongo Postgres para el outbox",
			Source: domain.SourceInput{
				Type:           "telegram",
				ExternalID:     "9999",
				IdempotencyKey: "9999",
				Metadata:       domain.Metadata{"chat_id": int64(4242)},
				CapturedAt:     time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
			},
			Object: domain.ObjectInput{Type: "document", CreatedBy: "u"},
		},
		Collision: app.Collision{
			Object:     domain.KnowledgeObject{ID: uuid.New(), Content: "ya se habló de esto"},
			Similarity: 0.81,
			Verdict:    app.CollisionStrongOverlap,
		},
	}
}

func TestPendingValidationStoreSaveAndTake(t *testing.T) {
	store := NewPendingValidationStore()
	entry := sampleEntry()
	ctx := context.Background()

	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Take(ctx, entry.Token)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got.Token != entry.Token {
		t.Errorf("Token = %q, want %q", got.Token, entry.Token)
	}
	if got.ChatID != entry.ChatID {
		t.Errorf("ChatID = %d, want %d", got.ChatID, entry.ChatID)
	}
	if got.Request.Content != entry.Request.Content {
		t.Errorf("Request.Content round-trip mismatch")
	}
	if got.Collision.Verdict != entry.Collision.Verdict {
		t.Errorf("Collision.Verdict = %q, want %q", got.Collision.Verdict, entry.Collision.Verdict)
	}
}

func TestPendingValidationStoreTakeUnknown(t *testing.T) {
	store := NewPendingValidationStore()
	_, err := store.Take(context.Background(), "ghost")
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Take(unknown) = %v, want app.ErrNotFound", err)
	}
}

func TestPendingValidationStoreTakeIsSingleUse(t *testing.T) {
	store := NewPendingValidationStore()
	entry := sampleEntry()
	ctx := context.Background()
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.Take(ctx, entry.Token); err != nil {
		t.Fatalf("first Take: %v", err)
	}
	_, err := store.Take(ctx, entry.Token)
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("second Take: err = %v, want app.ErrNotFound (load-and-delete)", err)
	}
}

// Take must be safe under concurrent calls: exactly one of two racing
// Take calls for the same token wins. The store is the gate that
// stops a double-tapped button from ingesting twice, so the race must
// resolve to a single winner.
func TestPendingValidationStoreTakeIsConcurrencySafe(t *testing.T) {
	store := NewPendingValidationStore()
	entry := sampleEntry()
	ctx := context.Background()
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	const racers = 8
	var wg sync.WaitGroup
	wg.Add(racers)
	wins := make(chan struct{}, racers)
	losses := make(chan struct{}, racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			if _, err := store.Take(ctx, entry.Token); err == nil {
				wins <- struct{}{}
			} else {
				losses <- struct{}{}
			}
		}()
	}
	wg.Wait()
	close(wins)
	close(losses)
	if len(wins) != 1 {
		t.Errorf("winners = %d, want exactly 1 (single-use contract)", len(wins))
	}
	if len(losses) != racers-1 {
		t.Errorf("losers = %d, want %d", len(losses), racers-1)
	}
}

// Take on an expired entry must look like "not found" so the handler
// reports "no longer available" instead of ingesting a stale prompt.
// The entry must also be removed (load-and-delete), so a second Take
// on the same token still returns ErrNotFound even after time moves on.
func TestPendingValidationStoreTakeExpiredReturnsNotFound(t *testing.T) {
	store := NewPendingValidationStore()
	entry := sampleEntry()
	entry.ExpiresAt = time.Now().Add(-1 * time.Minute) // already past
	ctx := context.Background()
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, err := store.Take(ctx, entry.Token)
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Take on expired entry: err = %v, want app.ErrNotFound", err)
	}
	// The row was still consumed by Take; a follow-up Take must
	// also report ErrNotFound rather than resurrecting it.
	if _, err := store.Take(ctx, entry.Token); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("second Take on expired token: err = %v, want app.ErrNotFound", err)
	}
}

// A zero ExpiresAt means "no expiry" — a defence-in-depth check that
// the TTL filter is opt-in, so tests and migrations that seed a row
// without a cutoff can still exercise the path.
func TestPendingValidationStoreTakeZeroExpiresAtNeverExpires(t *testing.T) {
	store := NewPendingValidationStore()
	entry := sampleEntry()
	// entry.ExpiresAt left as the zero value on purpose.
	ctx := context.Background()
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Take(ctx, entry.Token)
	if err != nil {
		t.Fatalf("Take with zero ExpiresAt: %v", err)
	}
	if got.Token != entry.Token {
		t.Errorf("Token = %q, want %q", got.Token, entry.Token)
	}
}

// Save is concurrent-safe so two simultaneous prompts cannot
// interleave on the map. With a fresh token per prompt this is
// theoretical, but the contract must be total.
func TestPendingValidationStoreSaveIsConcurrencySafe(t *testing.T) {
	store := NewPendingValidationStore()
	const writers = 16
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			entry := sampleEntry()
			entry.Token = uuid.NewString()
			entry.Request.Content = "writer-" + uuid.NewString()
			if err := store.Save(context.Background(), entry); err != nil {
				t.Errorf("Save[%d]: %v", i, err)
			}
		}()
	}
	wg.Wait()
	// A sanity read: every entry we wrote must be retrievable.
	// (We don't keep a registry, so we just exercise that the store
	// did not panic and Take still works on a fresh entry.)
	if _, err := store.Take(context.Background(), "definitely-not-there"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("Take on empty token: err = %v, want app.ErrNotFound", err)
	}
}
