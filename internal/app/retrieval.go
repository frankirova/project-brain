package app

import (
	"context"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
)

// SearchQuery is the input to a Retriever. Text is the raw query
// (already normalized: trimmed, lowercased). Limit caps the number
// of results; 0 means use the default (10). WorkspaceID scopes the
// search to a single tenant.
type SearchQuery struct {
	Text        string
	WorkspaceID string
	Limit       int
}

// SearchResult is one hit from a Retriever. Score is the relevance
// ranking (interpretation depends on the implementation: ts_rank for
// FTS, cosine distance for embeddings, etc.). MatchType hints at which
// retriever produced the hit.
type SearchResult struct {
	Object    domain.KnowledgeObject
	Score     float64
	MatchType string
}

// Retriever returns knowledge objects matching a SearchQuery. The
// current implementation is FTS-only; Fase 2's hybrid retrieval will
// introduce a composite that merges FTS + vector + structured filters.
//
// The Retriever is intentionally separate from IngestionUnitOfWork:
// read paths and write paths have different lifecycles, different
// connection pool pressures, and different failure modes. Keeping
// them as separate ports also lets a future Retriever delegate to a
// vector store or external search engine without dragging the
// ingestion UoW along.
type Retriever interface {
	Search(ctx context.Context, q SearchQuery) ([]SearchResult, error)
}

// DefaultSearchLimit is the cap applied when SearchQuery.Limit is 0.
const DefaultSearchLimit = 10

// MaxSearchLimit is a sanity bound to prevent unbounded result sets
// from a single HTTP request.
const MaxSearchLimit = 100

// ScoredSearchHit is the common shape produced by individual
// retriever implementations (FTS, vector, structured) before the
// composite merges and reranks them.
type ScoredSearchHit struct {
	ObjectID  string
	Score     float64
	MatchType string
	// FoundAt is recorded by the implementation; the composite uses
	// it for observability of retrieval freshness.
	FoundAt time.Time
}

// Embedder generates a vector representation of a text. The
// implementation is intentionally not specified here — it could be
// an OpenAI HTTP call, a local ONNX runtime, or a stub for tests.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	// Dimensions is the vector size this embedder produces. Used by
	// the repository to validate before insert.
	Dimensions() int
	// Model is the identifier persisted in embeddings.model. Useful
	// for distinguishing embeddings from different providers when
	// reranking or debugging.
	Model() string
}

// EmbeddingRepository persists and reads vectors. Search uses cosine
// distance via pgvector's <=> operator.
type EmbeddingRepository interface {
	// Upsert inserts or replaces the embedding for (workspace_id, object_id).
	Upsert(ctx context.Context, emb domain.Embedding) error
	// FindSimilar returns the workspace's objects closest to the query
	// vector by cosine distance. Limit caps the result count.
	FindSimilar(ctx context.Context, workspaceID string, query []float32, limit int) ([]ScoredSearchHit, error)
}
