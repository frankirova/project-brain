package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
)

// NewEmbeddingHook returns a PostIngestHook that embeds the object's
// textual content via the Embedder and upserts the resulting vector
// through the EmbeddingRepository.
//
// It is best-effort by contract: IngestTextService logs and swallows any
// error this returns, so a provider outage (e.g. Gemini quota) degrades
// future semantic search recall but never blocks ingestion. The knowledge
// object is already durably committed by the time this runs.
//
// For deployments that want the failure to be recoverable instead of
// dropped, see NewRetryAwareEmbeddingHook.
func NewEmbeddingHook(embedder Embedder, repo EmbeddingRepository) PostIngestHook {
	return func(ctx context.Context, obj domain.KnowledgeObject) error {
		text := embedText(obj)
		if text == "" {
			return nil
		}
		vec, err := embedder.Embed(ctx, text)
		if err != nil {
			return fmt.Errorf("embed object %s: %w", obj.ID, err)
		}
		return repo.Upsert(ctx, domain.Embedding{
			ObjectID:    obj.ID,
			WorkspaceID: obj.WorkspaceID,
			Model:       embedder.Model(),
			Dimensions:  embedder.Dimensions(),
			Vector:      vec,
		})
	}
}

// NewRetryAwareEmbeddingHook returns a PostIngestHook with the same
// success path as NewEmbeddingHook but with a recoverable failure
// path: on any embed or upsert error it enqueues a row in the
// embedding retry queue so a background worker can pick it up later.
//
// Semantics:
//
//   - The hook still returns the embed/upsert error so the existing
//     "post-ingest hook failed (best-effort, ingest unaffected)" log
//     in IngestTextService keeps firing. Ingest success is preserved.
//   - The enqueue itself is best-effort: if the job store is broken
//     (e.g. DB outage in the same outage that broke Gemini) we log
//     and continue. Dropping the retry is no worse than the pre-retry
//     behavior and the ingest still succeeds.
//   - Enqueue stores Attempts=1 (the hook just ran one failed try) and
//     NextRetryAt = now + EmbeddingRetryBackoff(1). The worker
//     increments Attempts on subsequent failures.
//   - Re-enqueueing the same (object_id, model) is an upsert at the
//     repository layer, so a duplicate ingest path running the hook
//     twice cannot create two rows.
func NewRetryAwareEmbeddingHook(embedder Embedder, repo EmbeddingRepository, jobs EmbeddingJobRepository, logger *slog.Logger) PostIngestHook {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, obj domain.KnowledgeObject) error {
		text := embedText(obj)
		if text == "" {
			return nil
		}
		hookErr := embedAndUpsert(ctx, embedder, repo, obj, text)
		if hookErr == nil {
			return nil
		}
		// At this point ingestion already committed. Enqueue a retry
		// job so the failure is not silent. The hook error is
		// returned to IngestTextService for the existing Warn log.
		job := EmbeddingJob{
			ObjectID:    obj.ID,
			WorkspaceID: obj.WorkspaceID,
			Model:       embedder.Model(),
			Attempts:    1,
			LastError:   truncateError(hookErr.Error()),
			NextRetryAt: time.Now().UTC().Add(EmbeddingRetryBackoff(1)),
		}
		if enqErr := jobs.Enqueue(ctx, job); enqErr != nil {
			logger.Error("enqueue embedding retry job failed",
				slog.String("workspace_id", obj.WorkspaceID),
				slog.String("object_id", obj.ID.String()),
				slog.String("model", embedder.Model()),
				slog.String("enqueue_error", enqErr.Error()),
				slog.String("embed_error", hookErr.Error()))
		}
		return hookErr
	}
}

// embedAndUpsert runs the embed + upsert pair shared by the plain and
// retry-aware hooks. text must already be non-empty.
func embedAndUpsert(ctx context.Context, embedder Embedder, repo EmbeddingRepository, obj domain.KnowledgeObject, text string) error {
	vec, err := embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed object %s: %w", obj.ID, err)
	}
	return repo.Upsert(ctx, domain.Embedding{
		ObjectID:    obj.ID,
		WorkspaceID: obj.WorkspaceID,
		Model:       embedder.Model(),
		Dimensions:  embedder.Dimensions(),
		Vector:      vec,
	})
}

// embedText joins the object's semantic fields (title, summary, content)
// into a single string for embedding, skipping empty parts. Title and
// summary add context that improves similarity matching beyond the raw
// content alone.
func embedText(obj domain.KnowledgeObject) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{obj.Title, obj.Summary, obj.Content} {
		if s := strings.TrimSpace(p); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// truncateError trims an error string before it lands in the
// embedding_jobs.last_error column. Postgres TEXT has no hard limit
// but a runaway provider response (e.g. an HTML error page) would
// bloat the row needlessly.
func truncateError(s string) string {
	const (
		max    = 2048
		suffix = "…(truncated)"
	)
	if len(s) <= max {
		return s
	}
	return s[:max-len(suffix)] + suffix
}
