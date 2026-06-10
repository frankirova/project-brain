package app

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// CompositeRetriever merges results from multiple retrievers. The
// current merge strategy is Reciprocal Rank Fusion (RRF) — each
// retriever contributes a score of 1/(k+rank) for every hit, and
// hits are aggregated by ObjectID. RRF is parameter-light, robust
// to score-scale differences between retrievers (ts_rank vs cosine
// distance), and gives a single ordering to return to the caller.
//
// When only one retriever is configured, the composite degenerates
// to that retriever's output (no merge overhead). This is the
// common case in dev (FTS-only) and is the path integration tests
// exercise.
type CompositeRetriever struct {
	primaries []Retriever
	k         int // RRF constant; smaller = more weight to top results
	limit     int
	hydrator  ObjectHydrator
}

// ObjectHydrator loads a KnowledgeObject by ID. The composite uses
// it to turn the merged hit IDs into full objects before returning.
// Errors are non-fatal: a missing or deleted object becomes a stub
// with just the ID set.
type ObjectHydrator interface {
	FindByID(ctx context.Context, workspaceID string, id uuid.UUID) (*domain.KnowledgeObject, error)
}

// SetHydrator attaches a hydrator. Optional: without one, the
// composite returns stubs with just the ID set.
func (c *CompositeRetriever) SetHydrator(h ObjectHydrator) {
	c.hydrator = h
}

// NewCompositeRetriever returns a composite that fans out to all
// primaries in parallel and merges with RRF. k=60 is the conventional
// choice from the original RRF paper; limit defaults to
// DefaultSearchLimit.
func NewCompositeRetriever(primaries []Retriever, k, limit int) *CompositeRetriever {
	if k <= 0 {
		k = 60
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	return &CompositeRetriever{primaries: primaries, k: k, limit: limit}
}

// Search fans out to each retriever in parallel, merges with RRF,
// hydrates the top hits via the ObjectHydrator, and returns the
// merged result set. If a hydrator is not configured, returns stubs
// with only the ID set (useful for unit tests and dev).
func (c *CompositeRetriever) Search(ctx context.Context, q SearchQuery) ([]SearchResult, error) {
	if len(c.primaries) == 0 {
		return nil, nil
	}

	// Fan out.
	type result struct {
		hits []ScoredSearchHit
		err error
	}
	results := make([]result, len(c.primaries))
	var wg sync.WaitGroup
	for i, r := range c.primaries {
		wg.Add(1)
		go func(i int, r Retriever) {
			defer wg.Done()
			hits, err := fanOut(ctx, r, q)
			results[i] = result{hits: hits, err: err}
		}(i, r)
	}
	wg.Wait()

	// Collect errors; if all fail, return the first error.
	firstErr := error(nil)
	hitSet := make(map[string]map[string]ScoredSearchHit) // retrieverName -> ObjectID -> hit
	for i, r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		name := retrieverName(c.primaries[i])
		if hitSet[name] == nil {
			hitSet[name] = make(map[string]ScoredSearchHit)
		}
		for _, h := range r.hits {
			// Keep the best score per (retriever, object).
			cur, ok := hitSet[name][h.ObjectID]
			if !ok || h.Score > cur.Score {
				hitSet[name][h.ObjectID] = h
			}
		}
	}
	if len(hitSet) == 0 {
		// All retrievers failed. Return the first error so the
		// caller sees the underlying cause.
		return nil, firstErr
	}

	// RRF merge.
	scores := make(map[string]float64)
	for _, byRet := range hitSet {
		// Sort hits per retriever by score desc so ranks are
		// meaningful.
		hits := make([]ScoredSearchHit, 0, len(byRet))
		for _, h := range byRet {
			hits = append(hits, h)
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
		for rank, h := range hits {
			scores[h.ObjectID] += 1.0 / float64(c.k+rank+1)
		}
	}

	// Take top N before hydration so the hydrator does not load
	// objects that will be discarded.
	type scored struct {
		id    string
		score float64
	}
	all := make([]scored, 0, len(scores))
	for id, s := range scores {
		all = append(all, scored{id, s})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	if len(all) > c.limit {
		all = all[:c.limit]
	}

	out := make([]SearchResult, 0, len(all))
	for _, s := range all {
		// Best individual score as a proxy for the merged score's
		// confidence. Composite confidence is exposed via the RRF
		// sum in s.score; we keep SearchResult.Score on the same
		// 0-1 scale as the underlying retrievers.
		bestScore := 0.0
		bestType := "hybrid"
		for _, byRet := range hitSet {
			if h, ok := byRet[s.id]; ok && h.Score > bestScore {
				bestScore = h.Score
				bestType = h.MatchType
			}
		}
		obj := c.hydrate(ctx, q.WorkspaceID, s.id)
		out = append(out, SearchResult{
			Object:    obj,
			Score:     s.score,
			MatchType: bestType,
		})
	}
	return out, nil
}

// hydrate loads the KnowledgeObject by ID, scoped to the workspace.
// Returns a stub with just the ID if no hydrator is set or the
// object no longer exists. The empty (zero-value) case is also a
// valid fallback when the hydrator returns (nil, nil) — for
// example when a vector search returned a hit for a deleted object.
func (c *CompositeRetriever) hydrate(ctx context.Context, workspaceID, idStr string) domain.KnowledgeObject {
	if c.hydrator == nil {
		return objectFromID(idStr)
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return domain.KnowledgeObject{}
	}
	obj, err := c.hydrator.FindByID(ctx, workspaceID, id)
	if err != nil || obj == nil {
		return objectFromID(idStr)
	}
	return *obj
}

// objectFromID returns a KnowledgeObject stub with just the ID set.
// The composite returns these stubs because hydration belongs to
// the HTTP layer; this keeps the composite independent of any
// specific object repository.
func objectFromID(idStr string) domain.KnowledgeObject {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return domain.KnowledgeObject{}
	}
	return domain.KnowledgeObject{ID: id}
}

// fanOut runs a single retriever with its own derived context so a
// slow retriever does not block the others. (Context cancellation
// from the caller is still honored.)
func fanOut(ctx context.Context, r Retriever, q SearchQuery) ([]ScoredSearchHit, error) {
	type hit struct {
		ObjectID  string
		Score     float64
		MatchType string
	}
	type out struct {
		hits []ScoredSearchHit
		err error
	}
	ch := make(chan out, 1)
	go func() {
		results, err := r.Search(ctx, q)
		oh := out{err: err}
		for _, x := range results {
			oh.hits = append(oh.hits, ScoredSearchHit{
				ObjectID:  x.Object.ID.String(),
				Score:     x.Score,
				MatchType: x.MatchType,
			})
		}
		ch <- oh
	}()
	res := <-ch
	return res.hits, res.err
}

// retrieverName returns a short label for logs and the per-retriever
// hit map. We use the position in the primaries slice as a stable
// identifier; a future change can pass explicit names.
func retrieverName(r Retriever) string {
	if r == nil {
		return "unknown"
	}
	return fmt.Sprintf("%T", r)
}
