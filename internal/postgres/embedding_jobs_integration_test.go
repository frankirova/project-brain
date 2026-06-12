package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ingestForEmbeddingJob creates a real knowledge_object so the
// embedding_jobs FK constraint is satisfied. Returns the object ID.
func ingestForEmbeddingJob(t *testing.T, db *DB, workspaceID, content string) uuid.UUID {
	t.Helper()
	svc := app.NewIngestTextService(db)
	res, err := svc.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: workspaceID,
		Content:     content,
		Object:      domain.ObjectInput{Type: "note"},
	})
	if err != nil {
		t.Fatalf("Ingest(%q): %v", content, err)
	}
	return res.ObjectID
}

// TestEmbeddingJobRepoEnqueueAndClaimRoundTrip verifies the basic
// enqueue → claim → delete flow with a real Postgres backend.
func TestEmbeddingJobRepoEnqueueAndClaimRoundTrip(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	objectID := ingestForEmbeddingJob(t, db, workspaceID, "embedding retry round trip")
	repo := NewEmbeddingJobRepo(db.pool)
	now := time.Now().UTC()

	job := app.EmbeddingJob{
		ObjectID:    objectID,
		WorkspaceID: workspaceID,
		Model:       "test-model",
		Attempts:    1,
		LastError:   "gemini quota exceeded",
		NextRetryAt: now.Add(-time.Minute), // due
	}
	if err := repo.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	due, err := repo.ClaimDue(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("ClaimDue returned %d jobs, want 1", len(due))
	}
	got := due[0]
	if got.ObjectID != objectID || got.Model != "test-model" {
		t.Errorf("identity wrong: %+v", got)
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
	if got.LastError != "gemini quota exceeded" {
		t.Errorf("LastError = %q, want %q", got.LastError, "gemini quota exceeded")
	}

	if err := repo.Delete(context.Background(), objectID, "test-model"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// After delete, ClaimDue should yield nothing for the workspace.
	stillDue, err := repo.ClaimDue(context.Background(), now.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("ClaimDue after delete: %v", err)
	}
	for _, j := range stillDue {
		if j.ObjectID == objectID {
			t.Errorf("deleted job still surfacing: %+v", j)
		}
	}
}

// TestEmbeddingJobRepoClaimLeasesRow exercises the FOR UPDATE SKIP
// LOCKED + next_retry_at lease bump. After a ClaimDue, a second
// ClaimDue at the same logical "now" must NOT return the same row
// because the first claim pushed next_retry_at forward.
func TestEmbeddingJobRepoClaimLeasesRow(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	objectID := ingestForEmbeddingJob(t, db, workspaceID, "lease test content")
	repo := NewEmbeddingJobRepo(db.pool)
	now := time.Now().UTC()

	if err := repo.Enqueue(context.Background(), app.EmbeddingJob{
		ObjectID:    objectID,
		WorkspaceID: workspaceID,
		Model:       "test-model",
		Attempts:    1,
		NextRetryAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Delete(context.Background(), objectID, "test-model")
	})

	firstClaim, err := repo.ClaimDue(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("first ClaimDue: %v", err)
	}
	if len(firstClaim) != 1 {
		t.Fatalf("first ClaimDue: got %d jobs, want 1", len(firstClaim))
	}

	// Same "now" — the row was due before the first claim. After the
	// lease bump it must not surface again at this clock value.
	secondClaim, err := repo.ClaimDue(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("second ClaimDue: %v", err)
	}
	for _, j := range secondClaim {
		if j.ObjectID == objectID {
			t.Errorf("leased job re-claimed at same clock: %+v", j)
		}
	}
}

// TestEmbeddingJobRepoEnqueueDeduplicates verifies the upsert
// guarantee: a second Enqueue for the same (object_id, model) updates
// the row instead of inserting a duplicate. Without this the hook's
// retry path would multiply jobs on every re-failed ingest.
func TestEmbeddingJobRepoEnqueueDeduplicates(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	objectID := ingestForEmbeddingJob(t, db, workspaceID, "dedup test content")
	repo := NewEmbeddingJobRepo(db.pool)
	now := time.Now().UTC()

	first := app.EmbeddingJob{
		ObjectID:    objectID,
		WorkspaceID: workspaceID,
		Model:       "test-model",
		Attempts:    1,
		LastError:   "first failure",
		NextRetryAt: now.Add(-time.Minute),
	}
	if err := repo.Enqueue(context.Background(), first); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Delete(context.Background(), objectID, "test-model")
	})

	second := first
	second.Attempts = 1 // hook always starts at 1; second-hand failure should not pile up
	second.LastError = "second failure"
	if err := repo.Enqueue(context.Background(), second); err != nil {
		t.Fatalf("second Enqueue: %v", err)
	}

	due, err := repo.ClaimDue(context.Background(), now, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	count := 0
	for _, j := range due {
		if j.ObjectID == objectID {
			count++
			if j.LastError != "second failure" {
				t.Errorf("LastError = %q, want updated %q", j.LastError, "second failure")
			}
		}
	}
	if count != 1 {
		t.Errorf("queue holds %d rows for object %s, want 1 (upsert)", count, objectID)
	}
}

// TestEmbeddingJobRepoMarkFailedUpdatesRow exercises the worker
// failure path: MarkFailed bumps attempts and pushes next_retry_at.
func TestEmbeddingJobRepoMarkFailedUpdatesRow(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	objectID := ingestForEmbeddingJob(t, db, workspaceID, "mark failed content")
	repo := NewEmbeddingJobRepo(db.pool)
	now := time.Now().UTC()

	if err := repo.Enqueue(context.Background(), app.EmbeddingJob{
		ObjectID:    objectID,
		WorkspaceID: workspaceID,
		Model:       "test-model",
		Attempts:    1,
		NextRetryAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Delete(context.Background(), objectID, "test-model")
	})

	future := now.Add(2 * time.Hour)
	if err := repo.MarkFailed(context.Background(), objectID, "test-model", 5, "still failing", future); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	// At a clock value between now and future, the row should not be due.
	mid := now.Add(time.Hour)
	due, err := repo.ClaimDue(context.Background(), mid, 10)
	if err != nil {
		t.Fatalf("ClaimDue mid: %v", err)
	}
	for _, j := range due {
		if j.ObjectID == objectID {
			t.Errorf("MarkFailed-pushed job surfaced at mid clock: %+v", j)
		}
	}

	// Past the future cutoff, ClaimDue returns the updated row.
	wayLater := future.Add(time.Minute)
	due, err = repo.ClaimDue(context.Background(), wayLater, 10)
	if err != nil {
		t.Fatalf("ClaimDue wayLater: %v", err)
	}
	var found *app.EmbeddingJob
	for i, j := range due {
		if j.ObjectID == objectID {
			found = &due[i]
		}
	}
	if found == nil {
		t.Fatalf("MarkFailed-pushed job missing after cutoff")
	}
	if found.Attempts != 5 {
		t.Errorf("Attempts = %d, want 5", found.Attempts)
	}
	if found.LastError != "still failing" {
		t.Errorf("LastError = %q, want %q", found.LastError, "still failing")
	}
}

// TestEmbeddingJobRepoDeleteMissingRowIsNoError covers the worker
// race path: two siblings claim the same row (in principle impossible
// after SKIP LOCKED, but a future bug could violate that), one
// deletes it, the other tries to delete and must not blow up.
func TestEmbeddingJobRepoDeleteMissingRowIsNoError(t *testing.T) {
	db := openIntegrationDB(t)
	repo := NewEmbeddingJobRepo(db.pool)
	if err := repo.Delete(context.Background(), uuid.New(), "never-existed"); err != nil {
		t.Fatalf("Delete on missing row: %v", err)
	}
}

// TestEmbeddingRetryServiceFullStackDrainsBacklog wires the real
// EmbeddingRetryService to a real Postgres job store + a stub
// embedder + the FTSRetriever as the object finder. The integration
// proves: an enqueued job for a real object, run through the worker,
// ends up with a vector in the embeddings table and the job row gone.
func TestEmbeddingRetryServiceFullStackDrainsBacklog(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	const retryContent = "full stack retry content"
	objectID := ingestForEmbeddingJob(t, db, workspaceID, retryContent)
	embedder := newStubEmbedder(map[string][]float32{
		retryContent: unitVec(10),
	})
	embeddingRepo := NewEmbeddingRepo(db.pool)
	jobRepo := NewEmbeddingJobRepo(db.pool)
	finder := NewFTSRetriever(db.pool)

	now := time.Now().UTC()
	if err := jobRepo.Enqueue(context.Background(), app.EmbeddingJob{
		ObjectID:    objectID,
		WorkspaceID: workspaceID,
		Model:       embedder.Model(),
		Attempts:    1,
		LastError:   "transient gemini outage",
		NextRetryAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	t.Cleanup(func() {
		_ = jobRepo.Delete(context.Background(), objectID, embedder.Model())
	})

	svc := app.NewEmbeddingRetryService(embedder, embeddingRepo, jobRepo, finder, nil, time.Now, 0)
	stats, err := svc.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if stats.Succeeded != 1 || stats.Claimed != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want claimed=1 succeeded=1", stats)
	}

	// Embedding row exists for the object now.
	vec := mustEmbed(t, embedder, retryContent)
	hits, err := embeddingRepo.FindSimilar(context.Background(), workspaceID, vec, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(hits) == 0 || hits[0].ObjectID != objectID.String() {
		t.Fatalf("expected vector for %s in FindSimilar results, got %+v", objectID, hits)
	}

	// Job row gone.
	farFuture := time.Now().UTC().Add(48 * time.Hour)
	due, err := jobRepo.ClaimDue(context.Background(), farFuture, 10)
	if err != nil {
		t.Fatalf("ClaimDue farFuture: %v", err)
	}
	for _, j := range due {
		if j.ObjectID == objectID {
			t.Errorf("completed job still in queue: %+v", j)
		}
	}
}
