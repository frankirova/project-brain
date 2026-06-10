package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FTSRetriever searches knowledge_objects using the generated
// tsvector column. Ranking uses ts_rank with the weight set
// (title=A, summary=B, tags=B, content=C) so a hit in the title
// outranks a hit in the content.
type FTSRetriever struct {
	pool *pgxpool.Pool
}

// NewFTSRetriever returns a Retriever backed by PostgreSQL full-text
// search. The pool is the same one used by DB; we keep them separate
// types so callers can compose a composite Retriever later without
// dragging the ingestion UoW.
func NewFTSRetriever(pool *pgxpool.Pool) *FTSRetriever {
	return &FTSRetriever{pool: pool}
}

// Search runs plainto_tsquery against the FTS column. Empty queries
// return an empty result set without touching the DB. The query
// string is trimmed and the limit is clamped to [1, MaxSearchLimit].
func (r *FTSRetriever) Search(ctx context.Context, q app.SearchQuery) ([]app.SearchResult, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, nil
	}
	workspaceID := strings.TrimSpace(q.WorkspaceID)
	if workspaceID == "" {
		return nil, errors.New("workspace_id is required for search")
	}

	limit := q.Limit
	if limit <= 0 {
		limit = app.DefaultSearchLimit
	}
	if limit > app.MaxSearchLimit {
		limit = app.MaxSearchLimit
	}

	const query = `
SELECT id, workspace_id, type, title, summary, content, status, metadata,
       created_by, created_at, updated_at, project_id, tags, confidence, importance,
       ts_rank(search_vector, plainto_tsquery('simple', $1)) AS score
FROM knowledge_objects
WHERE workspace_id = $2
  AND search_vector @@ plainto_tsquery('simple', $1)
ORDER BY score DESC, created_at DESC
LIMIT $3`

	rows, err := r.pool.Query(ctx, query, text, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []app.SearchResult
	for rows.Next() {
		var (
			obj         domain.KnowledgeObject
			score       float64
		)
		if err := rows.Scan(
			&obj.ID,
			&obj.WorkspaceID,
			&obj.Type,
			&obj.Title,
			&obj.Summary,
			&obj.Content,
			&obj.Status,
			&obj.Metadata,
			&obj.CreatedBy,
			&obj.CreatedAt,
			&obj.UpdatedAt,
			&obj.ProjectID,
			&obj.Tags,
			&obj.Confidence,
			&obj.Importance,
			&score,
		); err != nil {
			return nil, err
		}
		results = append(results, app.SearchResult{
			Object:    obj,
			Score:     score,
			MatchType: "fts",
		})
	}
	return results, rows.Err()
}

// FindByID returns a single knowledge object by ID and workspace.
// It is the read-side companion to the write path in DB: useful for
// the future "object detail" page without forcing every caller to
// instantiate the full UoW.
func (r *FTSRetriever) FindByID(ctx context.Context, workspaceID string, id uuid.UUID) (*domain.KnowledgeObject, error) {
	const query = `
SELECT id, workspace_id, type, title, summary, content, status, metadata,
       created_by, created_at, updated_at, project_id, tags, confidence, importance
FROM knowledge_objects
WHERE workspace_id = $1 AND id = $2`
	var (
		obj domain.KnowledgeObject
	)
	err := r.pool.QueryRow(ctx, query, workspaceID, id).Scan(
		&obj.ID, &obj.WorkspaceID, &obj.Type, &obj.Title, &obj.Summary, &obj.Content,
		&obj.Status, &obj.Metadata, &obj.CreatedBy, &obj.CreatedAt, &obj.UpdatedAt,
		&obj.ProjectID, &obj.Tags, &obj.Confidence, &obj.Importance,
	)
	if err != nil {
		return nil, err
	}
	return &obj, nil
}

// nowFunc is a clock seam for tests that want deterministic timestamps
// in SearchResult.
var nowFunc = time.Now
