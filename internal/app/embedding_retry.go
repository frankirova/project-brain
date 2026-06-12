package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Embedding retry tuning constants. Public so callers (composition
// root, tests) can introspect or override at wire time.
const (
	// MaxEmbeddingRetryAttempts is the soft cap before a job is
	// "paused": it is not deleted, but next_retry_at is pushed far
	// into the future so the worker stops spamming the provider and
	// an operator can see and clear it.
	MaxEmbeddingRetryAttempts = 12

	// MaxEmbeddingRetryBackoff caps the exponential growth so the
	// worst-case wait is bounded.
	MaxEmbeddingRetryBackoff = 6 * time.Hour

	// PausedEmbeddingRetryBackoff is the wait applied after
	// MaxEmbeddingRetryAttempts. The job stays visible in the table.
	PausedEmbeddingRetryBackoff = 24 * time.Hour

	// DefaultEmbeddingRetryBatch is the per-tick limit on jobs the
	// worker pulls. Small enough that one slow tick cannot saturate
	// the embedder; large enough to drain a normal backlog quickly.
	DefaultEmbeddingRetryBatch = 32

	// DefaultEmbeddingRetryInterval is the tick cadence between
	// ProcessDue runs when Start is used with interval=0.
	DefaultEmbeddingRetryInterval = 30 * time.Second
)

// EmbeddingRetryBackoff returns the delay before the next retry of a
// job that has just recorded its nth attempt (n ≥ 1). Exponential
// base-2 starting at 30s, capped at MaxEmbeddingRetryBackoff:
//
//	1 → 30s
//	2 → 1m
//	3 → 2m
//	4 → 4m
//	5 → 8m
//	6 → 16m
//	7 → 32m
//	8 → 64m
//	9 → 128m
//	10 → 256m
//	11+ → 6h (cap)
//
// Callers that hit MaxEmbeddingRetryAttempts should use
// PausedEmbeddingRetryBackoff instead so the job is parked rather
// than retried on the regular schedule.
func EmbeddingRetryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := 30 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= MaxEmbeddingRetryBackoff {
			return MaxEmbeddingRetryBackoff
		}
	}
	return d
}

// EmbeddingRetryService drains the embedding_jobs queue: on each tick
// it claims a batch of due jobs, re-runs the embed + upsert pair, and
// either deletes the row on success or records a new backoff on
// failure. The constructor wires the dependencies; ProcessDue runs one
// pass; Start spins a goroutine that calls ProcessDue on an interval
// until ctx is cancelled.
type EmbeddingRetryService struct {
	embedder Embedder
	repo     EmbeddingRepository
	jobs     EmbeddingJobRepository
	objects  KnowledgeObjectFinder
	now      Clock
	logger   *slog.Logger
	batch    int
}

// NewEmbeddingRetryService wires the dependencies. batch=0 falls back
// to DefaultEmbeddingRetryBatch; logger=nil falls back to
// slog.Default(); now=nil falls back to time.Now.
func NewEmbeddingRetryService(embedder Embedder, repo EmbeddingRepository, jobs EmbeddingJobRepository, objects KnowledgeObjectFinder, logger *slog.Logger, now Clock, batch int) *EmbeddingRetryService {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	if batch <= 0 {
		batch = DefaultEmbeddingRetryBatch
	}
	return &EmbeddingRetryService{
		embedder: embedder,
		repo:     repo,
		jobs:     jobs,
		objects:  objects,
		now:      now,
		logger:   logger,
		batch:    batch,
	}
}

// EmbeddingRetryStats is the per-tick outcome summary, useful for
// logs and tests.
type EmbeddingRetryStats struct {
	Claimed   int
	Succeeded int
	Failed    int
	Skipped   int
}

// ProcessDue runs a single drain pass: claim due jobs, retry each,
// record the result. Returns the per-tick stats and the first
// repository error if claiming failed (per-job errors are absorbed
// into the Failed counter, never returned). Safe to call from a
// scheduled goroutine or from a test.
func (s *EmbeddingRetryService) ProcessDue(ctx context.Context) (EmbeddingRetryStats, error) {
	now := s.now().UTC()
	jobs, err := s.jobs.ClaimDue(ctx, now, s.batch)
	if err != nil {
		return EmbeddingRetryStats{}, fmt.Errorf("claim due embedding jobs: %w", err)
	}
	stats := EmbeddingRetryStats{Claimed: len(jobs)}
	for _, job := range jobs {
		// Honor cancellation between jobs so a shutdown signal
		// stops a long batch from blocking server shutdown.
		if ctx.Err() != nil {
			return stats, nil
		}
		switch s.processOne(ctx, job) {
		case retryOutcomeSuccess:
			stats.Succeeded++
		case retryOutcomeFailed:
			stats.Failed++
		case retryOutcomeSkipped:
			stats.Skipped++
		}
	}
	return stats, nil
}

// retryOutcome is the result of attempting one job. Distinguishes
// "intentionally not retried" (object gone, model mismatch with the
// current embedder) from "tried and failed".
type retryOutcome int

const (
	retryOutcomeSuccess retryOutcome = iota
	retryOutcomeFailed
	retryOutcomeSkipped
)

func (s *EmbeddingRetryService) processOne(ctx context.Context, job EmbeddingJob) retryOutcome {
	// Skip jobs queued under a different model than the one this
	// worker is wired for. The other model's worker (or a future
	// migration) is responsible for them; deleting here would lose
	// retry history, retrying with this embedder would write a
	// mislabeled vector.
	if job.Model != s.embedder.Model() {
		s.logger.Debug("embedding retry skipped: model mismatch",
			slog.String("workspace_id", job.WorkspaceID),
			slog.String("object_id", job.ObjectID.String()),
			slog.String("job_model", job.Model),
			slog.String("worker_model", s.embedder.Model()))
		return retryOutcomeSkipped
	}

	obj, err := s.objects.FindByID(ctx, job.WorkspaceID, job.ObjectID)
	if err != nil {
		// Object disappeared between enqueue and retry — drop the
		// job. With the FK cascade in 0010 this should be rare,
		// but a manual delete or a non-Postgres backend could
		// still leave the job orphaned.
		if errors.Is(err, ErrNotFound) {
			s.logger.Info("embedding retry job dropped: object missing",
				slog.String("workspace_id", job.WorkspaceID),
				slog.String("object_id", job.ObjectID.String()))
			if delErr := s.jobs.Delete(ctx, job.ObjectID, job.Model); delErr != nil {
				s.logger.Warn("delete orphan embedding job failed",
					slog.String("object_id", job.ObjectID.String()),
					slog.String("error", delErr.Error()))
			}
			return retryOutcomeSkipped
		}
		s.recordFailure(ctx, job, fmt.Errorf("lookup object: %w", err))
		return retryOutcomeFailed
	}

	text := embedText(*obj)
	if text == "" {
		// Empty text means there's nothing to embed (the same
		// short-circuit the hook applies). Drop the job; a future
		// edit that adds content will re-enqueue on the hook path.
		s.logger.Info("embedding retry job dropped: empty text",
			slog.String("workspace_id", job.WorkspaceID),
			slog.String("object_id", job.ObjectID.String()))
		if delErr := s.jobs.Delete(ctx, job.ObjectID, job.Model); delErr != nil {
			s.logger.Warn("delete empty-text embedding job failed",
				slog.String("object_id", job.ObjectID.String()),
				slog.String("error", delErr.Error()))
		}
		return retryOutcomeSkipped
	}

	if err := embedAndUpsert(ctx, s.embedder, s.repo, *obj, text); err != nil {
		s.recordFailure(ctx, job, err)
		return retryOutcomeFailed
	}

	if err := s.jobs.Delete(ctx, job.ObjectID, job.Model); err != nil {
		// Vector was written; failing to delete the job means it
		// will be picked up again, run a redundant upsert, and
		// then succeed at delete next time. Log loudly but do not
		// treat the job as failed — the user-visible outcome
		// (vector exists) is correct.
		s.logger.Warn("delete completed embedding job failed",
			slog.String("workspace_id", job.WorkspaceID),
			slog.String("object_id", job.ObjectID.String()),
			slog.String("error", err.Error()))
	}
	s.logger.Info("embedding retry succeeded",
		slog.String("workspace_id", job.WorkspaceID),
		slog.String("object_id", job.ObjectID.String()),
		slog.Int("attempts", job.Attempts+1))
	return retryOutcomeSuccess
}

// recordFailure bumps attempts, computes the next backoff, and
// persists. Errors persisting the failure are logged but not
// returned: ProcessDue keeps draining the batch.
func (s *EmbeddingRetryService) recordFailure(ctx context.Context, job EmbeddingJob, cause error) {
	nextAttempts := job.Attempts + 1
	var backoff time.Duration
	if nextAttempts >= MaxEmbeddingRetryAttempts {
		backoff = PausedEmbeddingRetryBackoff
	} else {
		backoff = EmbeddingRetryBackoff(nextAttempts)
	}
	nextAt := s.now().UTC().Add(backoff)
	if err := s.jobs.MarkFailed(ctx, job.ObjectID, job.Model, nextAttempts, truncateError(cause.Error()), nextAt); err != nil {
		s.logger.Error("mark embedding job failed write failed",
			slog.String("workspace_id", job.WorkspaceID),
			slog.String("object_id", job.ObjectID.String()),
			slog.String("cause", cause.Error()),
			slog.String("error", err.Error()))
		return
	}
	level := slog.LevelWarn
	if nextAttempts >= MaxEmbeddingRetryAttempts {
		level = slog.LevelError
	}
	s.logger.Log(ctx, level, "embedding retry attempt failed",
		slog.String("workspace_id", job.WorkspaceID),
		slog.String("object_id", job.ObjectID.String()),
		slog.Int("attempts", nextAttempts),
		slog.Duration("next_in", backoff),
		slog.String("error", cause.Error()))
}

// Start runs ProcessDue on a ticker until ctx is cancelled. interval=0
// falls back to DefaultEmbeddingRetryInterval. The returned channel
// is closed when the goroutine exits (use it to wait for clean
// shutdown).
func (s *EmbeddingRetryService) Start(ctx context.Context, interval time.Duration) <-chan struct{} {
	if interval <= 0 {
		interval = DefaultEmbeddingRetryInterval
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.logger.Info("embedding retry worker started",
			slog.Duration("interval", interval),
			slog.Int("batch", s.batch),
			slog.String("model", s.embedder.Model()))
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.logger.Info("embedding retry worker stopped")
				return
			case <-ticker.C:
				stats, err := s.ProcessDue(ctx)
				if err != nil {
					s.logger.Error("embedding retry tick failed",
						slog.String("error", err.Error()))
					continue
				}
				if stats.Claimed > 0 {
					s.logger.Info("embedding retry tick complete",
						slog.Int("claimed", stats.Claimed),
						slog.Int("succeeded", stats.Succeeded),
						slog.Int("failed", stats.Failed),
						slog.Int("skipped", stats.Skipped))
				}
			}
		}
	}()
	return done
}
