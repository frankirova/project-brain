package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// Collision is an existing knowledge object that is semantically close
// to a candidate input — a potential duplicate, overlap, or conflict a
// human should review before the input becomes canonical knowledge.
type Collision struct {
	Object     domain.KnowledgeObject `json:"object"`
	Similarity float64                `json:"similarity"`
	Verdict    string                 `json:"verdict"`
}

// Collision verdicts, assigned by similarity. The detector deliberately
// does NOT decide agreement vs. contradiction (that needs an LLM
// judgment); it flags "these are about the same thing, review them
// together", which is the human-in-the-loop contract from the roadmap.
const (
	CollisionDuplicate     = "duplicate"      // near-identical content
	CollisionStrongOverlap = "strong_overlap" // same topic; likely merge or conflict
	CollisionRelated       = "related"        // adjacent topic worth a look
)

// DefaultCollisionThreshold is the minimum cosine similarity for a hit
// to be surfaced as a collision.
const DefaultCollisionThreshold = 0.75

// CollisionDetector finds existing knowledge that semantically collides
// with a candidate text, reusing the embedding + vector-similarity
// machinery. It is the shared core that both the HTTP check endpoint and
// the Telegram review flow build on.
type CollisionDetector struct {
	embedder   Embedder
	embeddings EmbeddingRepository
	objects    ObjectHydrator
	threshold  float64
	limit      int
}

// NewCollisionDetector composes the detector. threshold<=0 defaults to
// DefaultCollisionThreshold; limit<=0 defaults to DefaultSearchLimit.
func NewCollisionDetector(embedder Embedder, embeddings EmbeddingRepository, objects ObjectHydrator, threshold float64, limit int) *CollisionDetector {
	if threshold <= 0 {
		threshold = DefaultCollisionThreshold
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	return &CollisionDetector{
		embedder:   embedder,
		embeddings: embeddings,
		objects:    objects,
		threshold:  threshold,
		limit:      limit,
	}
}

// Detect embeds text, finds the workspace's most similar objects, and
// returns those at or above the similarity threshold as collisions,
// most-similar first. Empty text yields no collisions.
//
// excludeID, when non-nil, drops a self-match — used when re-checking an
// object that is already stored and embedded.
func (d *CollisionDetector) Detect(ctx context.Context, workspaceID, text string, excludeID *uuid.UUID) ([]Collision, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	vec, err := d.embedder.Embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed candidate: %w", err)
	}
	hits, err := d.embeddings.FindSimilar(ctx, workspaceID, vec, d.limit)
	if err != nil {
		return nil, err
	}

	collisions := make([]Collision, 0, len(hits))
	for _, h := range hits {
		if h.Score < d.threshold {
			continue
		}
		id, err := uuid.Parse(h.ObjectID)
		if err != nil {
			continue
		}
		if excludeID != nil && id == *excludeID {
			continue
		}
		obj, err := d.objects.FindByID(ctx, workspaceID, id)
		if err != nil || obj == nil {
			continue
		}
		collisions = append(collisions, Collision{
			Object:     *obj,
			Similarity: h.Score,
			Verdict:    collisionVerdict(h.Score),
		})
	}
	return collisions, nil
}

// Verdict bands, calibrated against real Gemini cosine similarities on
// Spanish text (gemini-embedding-001, 1536d). Observed scale:
//   - ~0.58-0.64  unrelated topics (filtered out below DefaultCollisionThreshold)
//   - ~0.78-0.85  same topic / direct contradiction (a real collision)
//   - ~0.90+      near-identical restatement (a duplicate)
//
// The bands sit on those clusters so a topical clash reads as
// strong_overlap rather than being undersold as merely related.
func collisionVerdict(score float64) string {
	switch {
	case score >= 0.90:
		return CollisionDuplicate
	case score >= 0.78:
		return CollisionStrongOverlap
	default:
		return CollisionRelated
	}
}
