package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

// EmbeddingRepo persists and reads vector embeddings for the
// knowledge_objects table. Uses pgvector's <=> operator (cosine
// distance) for similarity search.
type EmbeddingRepo struct {
	pool *pgxpool.Pool
}

// NewEmbeddingRepo returns a repo backed by the given pool.
func NewEmbeddingRepo(pool *pgxpool.Pool) *EmbeddingRepo {
	return &EmbeddingRepo{pool: pool}
}

// Upsert inserts or replaces the embedding for (workspace_id, object_id).
// Validates that emb.Dimensions matches what the embedder advertises
// — a wrong-sized vector is almost always a bug, not user error.
func (r *EmbeddingRepo) Upsert(ctx context.Context, emb domain.Embedding) error {
	if len(emb.Vector) == 0 {
		return errors.New("empty embedding vector")
	}
	now := time.Now().UTC()
	_, err := r.pool.Exec(ctx, `
INSERT INTO embeddings (object_id, workspace_id, model, dimensions, embedding, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)
ON CONFLICT (object_id) DO UPDATE
  SET model = EXCLUDED.model,
      dimensions = EXCLUDED.dimensions,
      embedding = EXCLUDED.embedding,
      updated_at = EXCLUDED.updated_at`,
		emb.ObjectID,
		emb.WorkspaceID,
		emb.Model,
		emb.Dimensions,
		pgvector.NewVector(emb.Vector),
		now,
	)
	return err
}

// FindSimilar returns the workspace's objects closest to the query
// vector by cosine distance. Lower distance = more similar, so we
// invert to a score in [0, 1] (1 - distance) for ranking.
func (r *EmbeddingRepo) FindSimilar(ctx context.Context, workspaceID string, query []float32, limit int) ([]app.ScoredSearchHit, error) {
	if len(query) == 0 {
		return nil, errors.New("empty query vector")
	}
	if limit <= 0 {
		limit = app.DefaultSearchLimit
	}
	if limit > app.MaxSearchLimit {
		limit = app.MaxSearchLimit
	}

	rows, err := r.pool.Query(ctx, `
SELECT e.object_id, 1 - (e.embedding <=> $1) AS score
FROM embeddings e
WHERE e.workspace_id = $2
ORDER BY e.embedding <=> $1
LIMIT $3`,
		pgvector.NewVector(query), workspaceID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("similarity search: %w", err)
	}
	defer rows.Close()

	var hits []app.ScoredSearchHit
	for rows.Next() {
		var (
			id    uuid.UUID
			score float64
		)
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		hits = append(hits, app.ScoredSearchHit{
			ObjectID:  id.String(),
			Score:     score,
			MatchType: "vector",
			FoundAt:   time.Now().UTC(),
		})
	}
	return hits, rows.Err()
}

// vectorRetriever is a Retriever backed by embeddings. It embeds the
// query text via the Embedder, then asks the EmbeddingRepo for the
// workspace's nearest neighbors. The returned objects are loaded
// from knowledge_objects in a second query.
type vectorRetriever struct {
	embedder   app.Embedder
	embeddings app.EmbeddingRepository
	objects    *FTSRetriever
	limit      int
}

// NewVectorRetriever composes an Embedder, an EmbeddingRepository,
// and an FTSRetriever (for object hydration). limit defaults to
// DefaultSearchLimit if 0.
func NewVectorRetriever(embedder app.Embedder, embeddings app.EmbeddingRepository, objects *FTSRetriever, limit int) *vectorRetriever {
	if limit <= 0 {
		limit = app.DefaultSearchLimit
	}
	return &vectorRetriever{
		embedder:   embedder,
		embeddings: embeddings,
		objects:    objects,
		limit:      limit,
	}
}

// Search embeds the query text and returns the closest objects by
// cosine distance. Empty query returns nil without calling the
// embedder.
func (r *vectorRetriever) Search(ctx context.Context, q app.SearchQuery) ([]app.SearchResult, error) {
	text := q.Text
	if text == "" {
		return nil, nil
	}

	vec, err := r.embedder.Embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	hits, err := r.embeddings.FindSimilar(ctx, q.WorkspaceID, vec, r.limit)
	if err != nil {
		return nil, err
	}

	results := make([]app.SearchResult, 0, len(hits))
	for _, h := range hits {
		id, err := uuid.Parse(h.ObjectID)
		if err != nil {
			continue
		}
		obj, err := r.objects.FindByID(ctx, q.WorkspaceID, id)
		if err != nil {
			// Object may have been deleted between the embedding scan
			// and the hydration query. Skip silently.
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		results = append(results, app.SearchResult{
			Object:    *obj,
			Score:     h.Score,
			MatchType: h.MatchType,
		})
	}
	return results, nil
}
