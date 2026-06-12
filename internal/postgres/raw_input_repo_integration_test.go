package postgres

import (
	"context"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cleanupRawInput deletes a raw_inputs row by ID. Best-effort: zero
// rows deleted is not an error (the test may have already consumed it).
func cleanupRawInput(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "DELETE FROM raw_inputs WHERE id = $1", id); err != nil {
		t.Fatalf("cleanup raw_input %s: %v", id, err)
	}
}

// sampleRawInput builds a pending raw_input suitable for tests.
func sampleRawInput(workspaceID string) domain.RawInput {
	return domain.RawInput{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		Channel:     domain.RawInputChannelTelegram,
		Content:     "propongo usar Postgres para el outbox",
		ActorID:     "user-123",
		ExternalRef: domain.Metadata{
			"chat_id":    int64(4242),
			"message_id": "9999",
		},
		Status: domain.RawInputStatusPending,
	}
}

// TestRawInputRepoCreate inserts a row and reads it back with a direct
// SELECT to assert all fields round-trip correctly.
func TestRawInputRepoCreate(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewRawInputRepo(db.pool)
	ctx := context.Background()

	ri := sampleRawInput("workspace-" + uuid.NewString())
	t.Cleanup(func() { cleanupRawInput(t, db.pool, ri.ID) })

	if err := repo.Create(ctx, ri); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var (
		gotID          uuid.UUID
		gotWorkspace   string
		gotChannel     string
		gotContent     string
		gotActorID     *string
		gotStatus      string
		gotCollSummary []byte
	)
	err := db.pool.QueryRow(ctx,
		`SELECT id, workspace_id, channel, content, actor_id, status, collision_summary
		 FROM raw_inputs WHERE id = $1`, ri.ID,
	).Scan(&gotID, &gotWorkspace, &gotChannel, &gotContent, &gotActorID, &gotStatus, &gotCollSummary)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	if gotID != ri.ID {
		t.Errorf("ID = %s, want %s", gotID, ri.ID)
	}
	if gotWorkspace != ri.WorkspaceID {
		t.Errorf("WorkspaceID = %q, want %q", gotWorkspace, ri.WorkspaceID)
	}
	if gotChannel != ri.Channel {
		t.Errorf("Channel = %q, want %q", gotChannel, ri.Channel)
	}
	if gotContent != ri.Content {
		t.Errorf("Content = %q, want %q", gotContent, ri.Content)
	}
	if gotActorID == nil || *gotActorID != ri.ActorID {
		t.Errorf("ActorID = %v, want %q", gotActorID, ri.ActorID)
	}
	if gotStatus != domain.RawInputStatusPending {
		t.Errorf("Status = %q, want %q", gotStatus, domain.RawInputStatusPending)
	}
	// collision_summary must be SQL NULL (not '{}') when not set
	if gotCollSummary != nil {
		t.Errorf("CollisionSummary = %q, want NULL", gotCollSummary)
	}
}

// TestRawInputRepoSetPromoted creates a pending row, promotes it, and
// asserts status and promoted_object_id are set with updated_at bumped.
func TestRawInputRepoSetPromoted(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewRawInputRepo(db.pool)
	ctx := context.Background()

	// We need a real knowledge_object for the FK on promoted_object_id.
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	svc := app.NewIngestTextService(db)
	res, err := svc.Ingest(ctx, domain.IngestTextRequest{
		WorkspaceID: workspaceID,
		Content:     "raw input promote test object",
		Object:      domain.ObjectInput{Type: "note"},
	})
	if err != nil {
		t.Fatalf("Ingest for FK: %v", err)
	}

	ri := sampleRawInput(workspaceID)
	t.Cleanup(func() { cleanupRawInput(t, db.pool, ri.ID) })

	if err := repo.Create(ctx, ri); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.SetPromoted(ctx, ri.ID, res.ObjectID); err != nil {
		t.Fatalf("SetPromoted: %v", err)
	}

	var gotStatus string
	var gotObjectID *uuid.UUID
	err = db.pool.QueryRow(ctx,
		`SELECT status, promoted_object_id FROM raw_inputs WHERE id = $1`, ri.ID,
	).Scan(&gotStatus, &gotObjectID)
	if err != nil {
		t.Fatalf("SELECT after SetPromoted: %v", err)
	}

	if gotStatus != domain.RawInputStatusPromoted {
		t.Errorf("Status = %q, want %q", gotStatus, domain.RawInputStatusPromoted)
	}
	if gotObjectID == nil || *gotObjectID != res.ObjectID {
		t.Errorf("PromotedObjectID = %v, want %s", gotObjectID, res.ObjectID)
	}
}

// TestRawInputRepoSetDiscarded creates a pending row, discards it, and
// asserts status is "discarded" and updated_at is bumped.
func TestRawInputRepoSetDiscarded(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewRawInputRepo(db.pool)
	ctx := context.Background()

	ri := sampleRawInput("workspace-" + uuid.NewString())
	t.Cleanup(func() { cleanupRawInput(t, db.pool, ri.ID) })

	if err := repo.Create(ctx, ri); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.SetDiscarded(ctx, ri.ID); err != nil {
		t.Fatalf("SetDiscarded: %v", err)
	}

	var gotStatus string
	err := db.pool.QueryRow(ctx,
		`SELECT status FROM raw_inputs WHERE id = $1`, ri.ID,
	).Scan(&gotStatus)
	if err != nil {
		t.Fatalf("SELECT after SetDiscarded: %v", err)
	}
	if gotStatus != domain.RawInputStatusDiscarded {
		t.Errorf("Status = %q, want %q", gotStatus, domain.RawInputStatusDiscarded)
	}
}

// TestRawInputRepoCreateWithCollisionSummary verifies that a non-nil
// CollisionSummary round-trips as JSONB (not NULL and not '{}').
func TestRawInputRepoCreateWithCollisionSummary(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewRawInputRepo(db.pool)
	ctx := context.Background()

	ri := sampleRawInput("workspace-" + uuid.NewString())
	ri.CollisionSummary = domain.Metadata{
		"verdict":         "strong_overlap",
		"similarity":      0.83,
		"object_id":       uuid.NewString(),
		"content_preview": "El equipo decidió usar Go",
	}
	t.Cleanup(func() { cleanupRawInput(t, db.pool, ri.ID) })

	if err := repo.Create(ctx, ri); err != nil {
		t.Fatalf("Create with collision_summary: %v", err)
	}

	var gotSummary []byte
	err := db.pool.QueryRow(ctx,
		`SELECT collision_summary FROM raw_inputs WHERE id = $1`, ri.ID,
	).Scan(&gotSummary)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if gotSummary == nil {
		t.Fatal("CollisionSummary = NULL, want non-NULL JSONB")
	}
	// Must not be an empty object
	if string(gotSummary) == "{}" {
		t.Error("CollisionSummary = '{}', want populated JSONB")
	}
}

// TestRawInputRepoSetCollisionSummary verifies SetCollisionSummary
// updates an existing row's collision_summary field.
func TestRawInputRepoSetCollisionSummary(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewRawInputRepo(db.pool)
	ctx := context.Background()

	ri := sampleRawInput("workspace-" + uuid.NewString())
	t.Cleanup(func() { cleanupRawInput(t, db.pool, ri.ID) })

	if err := repo.Create(ctx, ri); err != nil {
		t.Fatalf("Create: %v", err)
	}

	summary := domain.Metadata{
		"verdict":         "strong_overlap",
		"similarity":      0.90,
		"object_id":       uuid.NewString(),
		"content_preview": "El equipo decidió adoptar Go",
	}
	if err := repo.SetCollisionSummary(ctx, ri.ID, summary); err != nil {
		t.Fatalf("SetCollisionSummary: %v", err)
	}

	var gotSummary []byte
	err := db.pool.QueryRow(ctx,
		`SELECT collision_summary FROM raw_inputs WHERE id = $1`, ri.ID,
	).Scan(&gotSummary)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if gotSummary == nil {
		t.Fatal("collision_summary = NULL after SetCollisionSummary, want non-NULL")
	}
	if string(gotSummary) == "{}" {
		t.Error("collision_summary = '{}', want populated JSONB")
	}
}
