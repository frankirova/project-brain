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
