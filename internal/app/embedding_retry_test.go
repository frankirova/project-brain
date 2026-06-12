package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// fakeEmbeddingJobStore is an in-memory EmbeddingJobRepository for
// hook and worker tests. The behavior matches the contract on
// internal/app/ports.go: Enqueue is upsert by (object_id, model),
// ClaimDue returns the recorded due rows (no real lease semantics —
// the worker tests drive the clock explicitly), MarkFailed updates
// the row in place, Delete removes it.
type fakeEmbeddingJobStore struct {
	mu         sync.Mutex
	jobs       map[string]EmbeddingJob
	claimErr   error
	enqueueErr error
	markErr    error
	deleteErr  error
}

func newFakeEmbeddingJobStore() *fakeEmbeddingJobStore {
	return &fakeEmbeddingJobStore{jobs: map[string]EmbeddingJob{}}
}

func jobKey(objectID uuid.UUID, model string) string {
	return objectID.String() + "|" + model
}

func (s *fakeEmbeddingJobStore) Enqueue(_ context.Context, job EmbeddingJob) error {
	if s.enqueueErr != nil {
		return s.enqueueErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[jobKey(job.ObjectID, job.Model)] = job
	return nil
}

func (s *fakeEmbeddingJobStore) ClaimDue(_ context.Context, now time.Time, limit int) ([]EmbeddingJob, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []EmbeddingJob
	for _, j := range s.jobs {
		if !j.NextRetryAt.After(now) {
			due = append(due, j)
		}
	}
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

func (s *fakeEmbeddingJobStore) MarkFailed(_ context.Context, objectID uuid.UUID, model string, attempts int, lastErr string, nextRetryAt time.Time) error {
	if s.markErr != nil {
		return s.markErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := jobKey(objectID, model)
	j, ok := s.jobs[k]
	if !ok {
		return nil
	}
	j.Attempts = attempts
	j.LastError = lastErr
	j.NextRetryAt = nextRetryAt
	s.jobs[k] = j
	return nil
}

func (s *fakeEmbeddingJobStore) Delete(_ context.Context, objectID uuid.UUID, model string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, jobKey(objectID, model))
	return nil
}

func (s *fakeEmbeddingJobStore) snapshot() []EmbeddingJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]EmbeddingJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	return out
}

// fakeObjectFinder satisfies KnowledgeObjectFinder. Missing returns
// ErrNotFound; otherwise it returns the stored object.
type fakeObjectFinder struct {
	objects map[string]domain.KnowledgeObject
	err     error
}

func (f *fakeObjectFinder) FindByID(_ context.Context, workspaceID string, id uuid.UUID) (*domain.KnowledgeObject, error) {
	if f.err != nil {
		return nil, f.err
	}
	obj, ok := f.objects[workspaceID+"|"+id.String()]
	if !ok {
		return nil, ErrNotFound
	}
	return &obj, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestEmbeddingRetryBackoffSchedule pins the documented schedule so a
// well-meaning future refactor cannot silently widen the gap between
// the hook's first failure and the worker's first retry.
func TestEmbeddingRetryBackoffSchedule(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 30 * time.Second}, // floor: <1 clamps to 1
		{1, 30 * time.Second},
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{4, 240 * time.Second},
		{5, 480 * time.Second},
		{6, 960 * time.Second},
		{7, 1920 * time.Second},
		{8, 3840 * time.Second},
		{11, MaxEmbeddingRetryBackoff}, // cap
		{50, MaxEmbeddingRetryBackoff}, // cap holds far above
	}
	for _, tc := range cases {
		got := EmbeddingRetryBackoff(tc.attempts)
		if got != tc.want {
			t.Errorf("EmbeddingRetryBackoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

// TestRetryAwareEmbeddingHookEnqueuesOnEmbedFailure verifies the
// failure path: the embedder fails, the hook returns the error, and a
// job with attempts=1 lands in the store.
func TestRetryAwareEmbeddingHookEnqueuesOnEmbedFailure(t *testing.T) {
	emb := &fakeEmbedder{err: errors.New("quota exceeded"), vec: nil}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	hook := NewRetryAwareEmbeddingHook(emb, repo, jobs, quietLogger())

	id := uuid.New()
	err := hook(context.Background(), domain.KnowledgeObject{
		ID:          id,
		WorkspaceID: "ws-1",
		Title:       "T",
		Content:     "C",
	})
	if err == nil {
		t.Fatal("hook returned nil error, want wrapped embed error")
	}
	snap := jobs.snapshot()
	if len(snap) != 1 {
		t.Fatalf("jobs = %d, want 1", len(snap))
	}
	job := snap[0]
	if job.ObjectID != id {
		t.Errorf("job.ObjectID = %s, want %s", job.ObjectID, id)
	}
	if job.WorkspaceID != "ws-1" {
		t.Errorf("job.WorkspaceID = %q, want ws-1", job.WorkspaceID)
	}
	if job.Model != "fake-model" {
		t.Errorf("job.Model = %q, want fake-model", job.Model)
	}
	if job.Attempts != 1 {
		t.Errorf("job.Attempts = %d, want 1 (one failed try by the hook)", job.Attempts)
	}
	if job.LastError == "" {
		t.Error("job.LastError empty, want embed error captured")
	}
	if job.NextRetryAt.IsZero() {
		t.Error("job.NextRetryAt zero, want now + 30s")
	}
}

// TestRetryAwareEmbeddingHookEnqueuesOnUpsertFailure verifies the
// upsert-side failure also enqueues: a Gemini success followed by a
// transient repo write error must not silently drop the vector.
func TestRetryAwareEmbeddingHookEnqueuesOnUpsertFailure(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	repo := &fakeEmbeddingRepo{err: errors.New("pg down")}
	jobs := newFakeEmbeddingJobStore()
	hook := NewRetryAwareEmbeddingHook(emb, repo, jobs, quietLogger())

	id := uuid.New()
	err := hook(context.Background(), domain.KnowledgeObject{
		ID:          id,
		WorkspaceID: "ws-1",
		Content:     "C",
	})
	if err == nil {
		t.Fatal("hook returned nil error, want repo error")
	}
	if got := len(jobs.snapshot()); got != 1 {
		t.Fatalf("jobs = %d, want 1", got)
	}
}

// TestRetryAwareEmbeddingHookSkipsOnSuccess verifies the happy path
// does not pollute the queue.
func TestRetryAwareEmbeddingHookSkipsOnSuccess(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	hook := NewRetryAwareEmbeddingHook(emb, repo, jobs, quietLogger())

	err := hook(context.Background(), domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "ws-1",
		Content:     "C",
	})
	if err != nil {
		t.Fatalf("hook returned %v, want nil", err)
	}
	if got := len(jobs.snapshot()); got != 0 {
		t.Fatalf("jobs = %d, want 0 (success must not enqueue)", got)
	}
	if got := len(repo.upserted); got != 1 {
		t.Fatalf("upserts = %d, want 1", got)
	}
}

// TestRetryAwareEmbeddingHookSkipsEmptyText preserves the existing
// "no text → no work" short-circuit of the plain hook.
func TestRetryAwareEmbeddingHookSkipsEmptyText(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{1}}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	hook := NewRetryAwareEmbeddingHook(emb, repo, jobs, quietLogger())

	err := hook(context.Background(), domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "ws-1",
	})
	if err != nil {
		t.Fatalf("hook returned %v, want nil", err)
	}
	if got := len(jobs.snapshot()); got != 0 {
		t.Fatalf("jobs = %d, want 0 (empty text must not enqueue)", got)
	}
}

// TestRetryAwareEmbeddingHookEnqueueFailureSwallowed verifies the
// secondary safety net: even when the job store itself is broken,
// the hook still returns the original embed error (so the existing
// log path fires) and does not panic.
func TestRetryAwareEmbeddingHookEnqueueFailureSwallowed(t *testing.T) {
	emb := &fakeEmbedder{err: errors.New("quota")}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	jobs.enqueueErr = errors.New("pg also down")

	hook := NewRetryAwareEmbeddingHook(emb, repo, jobs, quietLogger())
	err := hook(context.Background(), domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "ws-1",
		Content:     "C",
	})
	if err == nil {
		t.Fatal("hook returned nil error, want embed error preserved")
	}
}

// TestEmbeddingRetryServiceProcessDueSuccessDeletesJob covers the
// happy path of a retry: the embedder returns a vector, the repo
// upserts it, the job is deleted.
func TestEmbeddingRetryServiceProcessDueSuccessDeletesJob(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "ws-1",
		Title:       "T",
		Content:     "C",
	}
	finder := &fakeObjectFinder{objects: map[string]domain.KnowledgeObject{
		"ws-1|" + obj.ID.String(): obj,
	}}
	emb := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	jobs.jobs[jobKey(obj.ID, emb.Model())] = EmbeddingJob{
		ObjectID:    obj.ID,
		WorkspaceID: obj.WorkspaceID,
		Model:       emb.Model(),
		Attempts:    1,
		LastError:   "previous quota error",
		NextRetryAt: time.Now().Add(-time.Minute), // due
	}

	svc := NewEmbeddingRetryService(emb, repo, jobs, finder, quietLogger(), time.Now, 0)
	stats, err := svc.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if stats.Succeeded != 1 || stats.Claimed != 1 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want claimed=1 succeeded=1 failed=0", stats)
	}
	if got := len(jobs.snapshot()); got != 0 {
		t.Errorf("job survived: %d remaining, want 0", got)
	}
	if got := len(repo.upserted); got != 1 {
		t.Errorf("upserts = %d, want 1", got)
	}
}

// TestEmbeddingRetryServiceProcessDueFailureKeepsJobAndBackoffs is
// the central worker contract: an embed error must NOT delete the
// job — it must MarkFailed with attempts+1 and the next backoff.
func TestEmbeddingRetryServiceProcessDueFailureKeepsJobAndBackoffs(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "ws-1",
		Content:     "C",
	}
	finder := &fakeObjectFinder{objects: map[string]domain.KnowledgeObject{
		"ws-1|" + obj.ID.String(): obj,
	}}
	emb := &fakeEmbedder{err: errors.New("still down")}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	// Pin the clock so the backoff check is deterministic. Use a
	// fixed NextRetryAt before the clock so ClaimDue surfaces it.
	fixedNow := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	jobs.jobs[jobKey(obj.ID, emb.Model())] = EmbeddingJob{
		ObjectID:    obj.ID,
		WorkspaceID: obj.WorkspaceID,
		Model:       emb.Model(),
		Attempts:    1,
		NextRetryAt: fixedNow.Add(-time.Minute),
	}

	svc := NewEmbeddingRetryService(emb, repo, jobs, finder, quietLogger(), func() time.Time { return fixedNow }, 0)

	stats, err := svc.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if stats.Failed != 1 || stats.Succeeded != 0 {
		t.Errorf("stats = %+v, want failed=1 succeeded=0", stats)
	}
	snap := jobs.snapshot()
	if len(snap) != 1 {
		t.Fatalf("job count = %d, want 1 (failure must not delete)", len(snap))
	}
	got := snap[0]
	if got.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", got.Attempts)
	}
	if got.LastError == "" {
		t.Error("last_error empty, want embed error captured")
	}
	wantNext := fixedNow.Add(EmbeddingRetryBackoff(2))
	if !got.NextRetryAt.Equal(wantNext) {
		t.Errorf("NextRetryAt = %s, want %s (now + backoff(2))", got.NextRetryAt, wantNext)
	}
}

// TestEmbeddingRetryServicePausesAtMaxAttempts covers the soft cap:
// after MaxEmbeddingRetryAttempts the job is parked (24h backoff)
// instead of retried on the normal schedule.
func TestEmbeddingRetryServicePausesAtMaxAttempts(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "ws-1",
		Content:     "C",
	}
	finder := &fakeObjectFinder{objects: map[string]domain.KnowledgeObject{
		"ws-1|" + obj.ID.String(): obj,
	}}
	emb := &fakeEmbedder{err: errors.New("still down")}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	fixedNow := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	jobs.jobs[jobKey(obj.ID, emb.Model())] = EmbeddingJob{
		ObjectID:    obj.ID,
		WorkspaceID: obj.WorkspaceID,
		Model:       emb.Model(),
		Attempts:    MaxEmbeddingRetryAttempts - 1, // next failure crosses the cap
		NextRetryAt: fixedNow.Add(-time.Minute),
	}

	svc := NewEmbeddingRetryService(emb, repo, jobs, finder, quietLogger(), func() time.Time { return fixedNow }, 0)

	if _, err := svc.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	snap := jobs.snapshot()
	if len(snap) != 1 {
		t.Fatalf("job count = %d, want 1 (cap pauses, does not delete)", len(snap))
	}
	got := snap[0]
	if got.Attempts != MaxEmbeddingRetryAttempts {
		t.Errorf("attempts = %d, want %d (cap)", got.Attempts, MaxEmbeddingRetryAttempts)
	}
	wantNext := fixedNow.Add(PausedEmbeddingRetryBackoff)
	if !got.NextRetryAt.Equal(wantNext) {
		t.Errorf("NextRetryAt = %s, want %s (paused backoff)", got.NextRetryAt, wantNext)
	}
}

// TestEmbeddingRetryServiceMissingObjectDropsJob covers the orphan
// path: if the knowledge_object is gone, retrying is pointless and
// the row must be deleted so the worker stops looping on it.
func TestEmbeddingRetryServiceMissingObjectDropsJob(t *testing.T) {
	finder := &fakeObjectFinder{objects: map[string]domain.KnowledgeObject{}}
	emb := &fakeEmbedder{vec: []float32{0.1}}
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	objID := uuid.New()
	jobs.jobs[jobKey(objID, emb.Model())] = EmbeddingJob{
		ObjectID:    objID,
		WorkspaceID: "ws-1",
		Model:       emb.Model(),
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Minute),
	}

	svc := NewEmbeddingRetryService(emb, repo, jobs, finder, quietLogger(), time.Now, 0)
	stats, err := svc.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if stats.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", stats.Skipped)
	}
	if got := len(jobs.snapshot()); got != 0 {
		t.Errorf("orphan job survived: %d remaining, want 0", got)
	}
}

// TestEmbeddingRetryServiceModelMismatchSkips covers the safety
// guard: a job queued under a different model than the worker is
// wired for must be left alone, not retried and not deleted.
func TestEmbeddingRetryServiceModelMismatchSkips(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "ws-1",
		Content:     "C",
	}
	finder := &fakeObjectFinder{objects: map[string]domain.KnowledgeObject{
		"ws-1|" + obj.ID.String(): obj,
	}}
	emb := &fakeEmbedder{vec: []float32{0.1}} // Model() == "fake-model"
	repo := &fakeEmbeddingRepo{}
	jobs := newFakeEmbeddingJobStore()
	jobs.jobs[jobKey(obj.ID, "other-model")] = EmbeddingJob{
		ObjectID:    obj.ID,
		WorkspaceID: obj.WorkspaceID,
		Model:       "other-model",
		Attempts:    1,
		NextRetryAt: time.Now().Add(-time.Minute),
	}

	svc := NewEmbeddingRetryService(emb, repo, jobs, finder, quietLogger(), time.Now, 0)
	stats, err := svc.ProcessDue(context.Background())
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if stats.Skipped != 1 || stats.Succeeded != 0 || stats.Failed != 0 {
		t.Errorf("stats = %+v, want skipped=1", stats)
	}
	if got := len(jobs.snapshot()); got != 1 {
		t.Error("mismatched-model job was deleted, want preserved for the other model's worker")
	}
}

// TestEmbeddingRetryServiceClaimErrorBubbles verifies the worker
// surfaces a repository-level failure (e.g. DB down) instead of
// silently looping on nothing.
func TestEmbeddingRetryServiceClaimErrorBubbles(t *testing.T) {
	jobs := newFakeEmbeddingJobStore()
	jobs.claimErr = errors.New("pool exhausted")
	svc := NewEmbeddingRetryService(&fakeEmbedder{}, &fakeEmbeddingRepo{}, jobs, &fakeObjectFinder{}, quietLogger(), time.Now, 0)
	_, err := svc.ProcessDue(context.Background())
	if err == nil {
		t.Fatal("ProcessDue returned nil error, want claim failure wrapped")
	}
}
