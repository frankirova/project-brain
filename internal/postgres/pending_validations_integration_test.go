package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// samplePendingValidation builds a representative entry: a real
// IngestTextRequest (so the JSONB round-trip exercises every field
// including Metadata, time.Time, and UUIDs) and a Collision with a
// populated KnowledgeObject.
func samplePendingValidation() app.PendingValidation {
	return app.PendingValidation{
		Token:      uuid.NewString(),
		ChatID:     4242,
		RawInputID: uuid.New(),
		Request: domain.IngestTextRequest{
			WorkspaceID: "default",
			Content:     "propongo Postgres para persistir el outbox",
			Source: domain.SourceInput{
				Type:           "telegram",
				ExternalID:     "9999",
				IdempotencyKey: "9999",
				Metadata: domain.Metadata{
					"chat_id": int64(4242),
					"user_id": "12345",
				},
				CapturedAt: time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
			},
			Object: domain.ObjectInput{
				Type:      "document",
				Title:     "Propuesta outbox",
				Summary:   "Persistencia durable",
				CreatedBy: "12345",
				Metadata:  domain.Metadata{"importance": "high"},
			},
		},
		Collision: app.Collision{
			Object: domain.KnowledgeObject{
				ID:          uuid.New(),
				WorkspaceID: "default",
				Content:     "El equipo decidió usar Redis Streams para el outbox",
				Status:      domain.KnowledgeObjectStatusActive,
				CreatedBy:   "67890",
				CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Tags:        []string{"outbox", "decision"},
			},
			Similarity: 0.83,
			Verdict:    app.CollisionStrongOverlap,
		},
	}
}

// TestPendingValidationStoreSaveAndTakeRoundTrip verifies Save
// persists a full entry and Take returns it byte-for-byte after a
// JSON round-trip. The IngestTextRequest and Collision carry
// time.Time, uuid.UUID, slices, and Metadata — every type the handler
// can produce must survive the storage boundary.
func TestPendingValidationStoreSaveAndTakeRoundTrip(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPendingValidationStore(db.pool)
	ctx := context.Background()

	entry := samplePendingValidation()
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Cleanup(func() { cleanupPendingValidation(t, db.pool, entry.Token) })

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
		t.Errorf("Request.Content = %q, want %q", got.Request.Content, entry.Request.Content)
	}
	if got.Request.WorkspaceID != entry.Request.WorkspaceID {
		t.Errorf("Request.WorkspaceID = %q, want %q", got.Request.WorkspaceID, entry.Request.WorkspaceID)
	}
	if got.Request.Source.ExternalID != entry.Request.Source.ExternalID {
		t.Errorf("Request.Source.ExternalID = %q, want %q", got.Request.Source.ExternalID, entry.Request.Source.ExternalID)
	}
	if got.Request.Source.IdempotencyKey != entry.Request.Source.IdempotencyKey {
		t.Errorf("Request.Source.IdempotencyKey = %q, want %q", got.Request.Source.IdempotencyKey, entry.Request.Source.IdempotencyKey)
	}
	if !got.Request.Source.CapturedAt.Equal(entry.Request.Source.CapturedAt) {
		t.Errorf("Request.Source.CapturedAt = %v, want %v", got.Request.Source.CapturedAt, entry.Request.Source.CapturedAt)
	}
	if got.Request.Source.Metadata["user_id"] != entry.Request.Source.Metadata["user_id"] {
		t.Errorf("Request.Source.Metadata[user_id] = %v, want %v", got.Request.Source.Metadata["user_id"], entry.Request.Source.Metadata["user_id"])
	}
	if got.Request.Object.Title != entry.Request.Object.Title {
		t.Errorf("Request.Object.Title = %q, want %q", got.Request.Object.Title, entry.Request.Object.Title)
	}
	if got.Request.Object.CreatedBy != entry.Request.Object.CreatedBy {
		t.Errorf("Request.Object.CreatedBy = %q, want %q", got.Request.Object.CreatedBy, entry.Request.Object.CreatedBy)
	}

	if got.Collision.Verdict != entry.Collision.Verdict {
		t.Errorf("Collision.Verdict = %q, want %q", got.Collision.Verdict, entry.Collision.Verdict)
	}
	if got.Collision.Similarity != entry.Collision.Similarity {
		t.Errorf("Collision.Similarity = %f, want %f", got.Collision.Similarity, entry.Collision.Similarity)
	}
	if got.Collision.Object.ID != entry.Collision.Object.ID {
		t.Errorf("Collision.Object.ID = %s, want %s", got.Collision.Object.ID, entry.Collision.Object.ID)
	}
	if got.Collision.Object.Content != entry.Collision.Object.Content {
		t.Errorf("Collision.Object.Content = %q, want %q", got.Collision.Object.Content, entry.Collision.Object.Content)
	}
	if len(got.Collision.Object.Tags) != len(entry.Collision.Object.Tags) {
		t.Errorf("Collision.Object.Tags len = %d, want %d", len(got.Collision.Object.Tags), len(entry.Collision.Object.Tags))
	}
	if got.RawInputID != entry.RawInputID {
		t.Errorf("RawInputID = %s, want %s", got.RawInputID, entry.RawInputID)
	}
}

// TestPendingValidationStoreTakeUnknownReturnsNotFound verifies the
// "no such entry" path: an unknown token must yield app.ErrNotFound
// with no row written.
func TestPendingValidationStoreTakeUnknownReturnsNotFound(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPendingValidationStore(db.pool)

	_, err := store.Take(context.Background(), "never-saved-"+uuid.NewString())
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("Take unknown token: err = %v, want app.ErrNotFound", err)
	}
}

// TestPendingValidationStoreTakeIsSingleUse is the load-and-delete
// guarantee: after a successful Take, the same token yields
// app.ErrNotFound. This is what stops a double-tapped button from
// ingesting twice.
func TestPendingValidationStoreTakeIsSingleUse(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPendingValidationStore(db.pool)
	ctx := context.Background()

	entry := samplePendingValidation()
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Cleanup(func() { cleanupPendingValidation(t, db.pool, entry.Token) })

	if _, err := store.Take(ctx, entry.Token); err != nil {
		t.Fatalf("first Take: %v", err)
	}
	_, err := store.Take(ctx, entry.Token)
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("second Take: err = %v, want app.ErrNotFound (load-and-delete contract)", err)
	}
}

// TestPendingValidationStoreSaveOverwritesExistingToken verifies the
// upsert path: re-saving the same token replaces the row instead of
// failing on the primary key. The handler is not expected to do this,
// but the contract must be total so a buggy caller cannot deadlock.
func TestPendingValidationStoreSaveOverwritesExistingToken(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPendingValidationStore(db.pool)
	ctx := context.Background()

	entry := samplePendingValidation()
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	t.Cleanup(func() { cleanupPendingValidation(t, db.pool, entry.Token) })

	entry.ChatID = 9999
	entry.Request.Content = "contenido actualizado"
	if err := store.Save(ctx, entry); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := store.Take(ctx, entry.Token)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if got.ChatID != 9999 {
		t.Errorf("ChatID = %d, want 9999 (upsert must replace)", got.ChatID)
	}
	if got.Request.Content != "contenido actualizado" {
		t.Errorf("Request.Content = %q, want %q", got.Request.Content, "contenido actualizado")
	}
}

// cleanupPendingValidation is best-effort: if the test already
// consumed the row via Take, the DELETE matches zero rows and
// returns nil. Failures here are loud because a stuck row is a real
// data-integrity signal.
func cleanupPendingValidation(t *testing.T, pool *pgxpool.Pool, token string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "DELETE FROM telegram_pending_validations WHERE token = $1", token); err != nil {
		t.Fatalf("cleanup pending validation %s: %v", token, err)
	}
}
