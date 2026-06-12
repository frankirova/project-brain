package postgres

import (
	"context"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EmbeddingJobRepo is the PostgreSQL implementation of
// app.EmbeddingJobRepository, backed by migration 0010.
//
// ClaimDue uses FOR UPDATE SKIP LOCKED + an in-place lease bump on
// next_retry_at so it is safe for multiple workers without a separate
// locked_at column: a sibling worker scanning the same window will
// either skip the locked row (during the claiming tx) or see the
// already-pushed next_retry_at (after the tx commits) and skip it on
// the "due" filter. The lease window is intentionally larger than the
// expected per-job runtime so a slow embed call doesn't lose its
// claim mid-way.
type EmbeddingJobRepo struct {
	pool         *pgxpool.Pool
	leaseSeconds int
}

// EmbeddingJobLeaseSeconds is the default in-place push on
// next_retry_at applied to a claimed row, in seconds. Long enough that
// a slow embed (Gemini quota throttle, slow HTTP) doesn't lose its
// claim; short enough that a crashed worker's rows reappear within a
// few minutes.
const EmbeddingJobLeaseSeconds = 300

// NewEmbeddingJobRepo returns a repo backed by the given pool with
// the default lease window.
func NewEmbeddingJobRepo(pool *pgxpool.Pool) *EmbeddingJobRepo {
	return &EmbeddingJobRepo{pool: pool, leaseSeconds: EmbeddingJobLeaseSeconds}
}

// Enqueue inserts a new pending job or, when (object_id, model)
// already exists, overwrites attempts / last_error / next_retry_at
// and refreshes updated_at. The DO UPDATE branch is the dedup that
// the hook contract relies on: a duplicate ingest path running the
// hook twice produces one row, not two.
func (r *EmbeddingJobRepo) Enqueue(ctx context.Context, job app.EmbeddingJob) error {
	now := time.Now().UTC()
	var lastErr any
	if job.LastError != "" {
		lastErr = job.LastError
	}
	_, err := r.pool.Exec(ctx, `
INSERT INTO embedding_jobs (object_id, model, workspace_id, attempts, last_error, next_retry_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (object_id, model) DO UPDATE
  SET attempts = EXCLUDED.attempts,
      last_error = EXCLUDED.last_error,
      next_retry_at = EXCLUDED.next_retry_at,
      workspace_id = EXCLUDED.workspace_id,
      updated_at = EXCLUDED.updated_at`,
		job.ObjectID, job.Model, job.WorkspaceID, job.Attempts, lastErr, job.NextRetryAt, now,
	)
	return err
}

// ClaimDue returns up to limit jobs whose next_retry_at <= now,
// pushing their next_retry_at forward by the lease window in the
// same statement so siblings see the row as not-due. Rows are
// returned in next_retry_at ascending order so the oldest backlog
// drains first.
func (r *EmbeddingJobRepo) ClaimDue(ctx context.Context, now time.Time, limit int) ([]app.EmbeddingJob, error) {
	if limit <= 0 {
		limit = app.DefaultEmbeddingRetryBatch
	}
	leaseUntil := now.Add(time.Duration(r.leaseSeconds) * time.Second)

	// The CTE picks due rows with SKIP LOCKED, the UPDATE bumps
	// their next_retry_at to the lease cutoff, and RETURNING gives
	// the worker the persisted state (post-lease next_retry_at is
	// not returned to the worker since the worker either deletes or
	// MarkFailed-s with its own value).
	rows, err := r.pool.Query(ctx, `
WITH due AS (
    SELECT object_id, model
    FROM embedding_jobs
    WHERE next_retry_at <= $1
    ORDER BY next_retry_at
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE embedding_jobs e
SET next_retry_at = $3,
    updated_at = $1
FROM due
WHERE e.object_id = due.object_id AND e.model = due.model
RETURNING e.object_id, e.model, e.workspace_id, e.attempts,
          COALESCE(e.last_error, ''), e.created_at, e.updated_at`,
		now, limit, leaseUntil,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []app.EmbeddingJob
	for rows.Next() {
		var (
			job       app.EmbeddingJob
			lastErr   string
			createdAt time.Time
			updatedAt time.Time
		)
		if err := rows.Scan(
			&job.ObjectID, &job.Model, &job.WorkspaceID, &job.Attempts,
			&lastErr, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		job.LastError = lastErr
		job.CreatedAt = createdAt
		job.UpdatedAt = updatedAt
		// NextRetryAt is set to the lease window in the DB; the
		// worker doesn't read it (Delete or MarkFailed will
		// overwrite). Surface the lease cutoff anyway so callers
		// that inspect the slice see the durable state.
		job.NextRetryAt = leaseUntil
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// MarkFailed records a worker retry that failed: bump attempts,
// store the truncated error, and schedule the next try via
// next_retry_at. The row stays in the table (no DELETE) so the
// worker can pick it up again on the next due tick.
func (r *EmbeddingJobRepo) MarkFailed(ctx context.Context, objectID uuid.UUID, model string, attempts int, lastErr string, nextRetryAt time.Time) error {
	now := time.Now().UTC()
	var lastErrParam any
	if lastErr != "" {
		lastErrParam = lastErr
	}
	_, err := r.pool.Exec(ctx, `
UPDATE embedding_jobs
SET attempts = $1,
    last_error = $2,
    next_retry_at = $3,
    updated_at = $4
WHERE object_id = $5 AND model = $6`,
		attempts, lastErrParam, nextRetryAt, now, objectID, model,
	)
	return err
}

// Delete removes the job identified by (object_id, model). A missing
// row is not an error: a concurrent worker may have already deleted
// it, and the desired state ("no pending retry") is satisfied either
// way.
func (r *EmbeddingJobRepo) Delete(ctx context.Context, objectID uuid.UUID, model string) error {
	_, err := r.pool.Exec(ctx, `
DELETE FROM embedding_jobs WHERE object_id = $1 AND model = $2`,
		objectID, model,
	)
	return err
}

var _ app.EmbeddingJobRepository = (*EmbeddingJobRepo)(nil)
