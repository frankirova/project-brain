package app

import (
	"context"
	"errors"
	"testing"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

func hit(id string, score float64) ScoredSearchHit {
	return ScoredSearchHit{ObjectID: id, Score: score, MatchType: "vector"}
}

func TestDetectFiltersByThreshold(t *testing.T) {
	id1 := uuid.New().String()
	id2 := uuid.New().String()
	id3 := uuid.New().String()
	repo := &fakeEmbeddingRepo{similar: []ScoredSearchHit{
		hit(id1, 0.95), // above
		hit(id2, 0.85), // above
		hit(id3, 0.70), // below threshold 0.80
	}}
	det := NewCollisionDetector(
		&fakeEmbedder{vec: []float32{0.1, 0.2}},
		repo,
		&fakeHydrator{obj: &domain.KnowledgeObject{ID: uuid.New(), Content: "existing"}},
		0.80, 5,
	)

	got, err := det.Detect(context.Background(), "ws-1", "candidate text", nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("collisions = %d, want 2 (0.70 filtered out)", len(got))
	}
	if got[0].Similarity != 0.95 || got[1].Similarity != 0.85 {
		t.Fatalf("order/similarity wrong: %+v", got)
	}
}

func TestDetectAssignsVerdicts(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{0.97, CollisionDuplicate},
		{0.90, CollisionDuplicate},
		{0.85, CollisionStrongOverlap},
		{0.78, CollisionStrongOverlap},
		{0.77, CollisionRelated},
	}
	for _, c := range cases {
		repo := &fakeEmbeddingRepo{similar: []ScoredSearchHit{hit(uuid.New().String(), c.score)}}
		det := NewCollisionDetector(
			&fakeEmbedder{vec: []float32{0.1}},
			repo,
			&fakeHydrator{obj: &domain.KnowledgeObject{ID: uuid.New()}},
			0.5, 5,
		)
		got, err := det.Detect(context.Background(), "ws", "x", nil)
		if err != nil {
			t.Fatalf("Detect(%.2f): %v", c.score, err)
		}
		if len(got) != 1 || got[0].Verdict != c.want {
			t.Fatalf("score %.2f -> verdict %v, want %q", c.score, got, c.want)
		}
	}
}

func TestDetectEmptyTextReturnsNothing(t *testing.T) {
	det := NewCollisionDetector(&fakeEmbedder{}, &fakeEmbeddingRepo{}, &fakeHydrator{}, 0, 0)
	got, err := det.Detect(context.Background(), "ws", "   ", nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got != nil {
		t.Fatalf("collisions = %v, want nil for empty text", got)
	}
}

func TestDetectExcludesSelf(t *testing.T) {
	self := uuid.New()
	other := uuid.New()
	repo := &fakeEmbeddingRepo{similar: []ScoredSearchHit{
		hit(self.String(), 0.99),
		hit(other.String(), 0.90),
	}}
	det := NewCollisionDetector(
		&fakeEmbedder{vec: []float32{0.1}},
		repo,
		&fakeHydrator{obj: &domain.KnowledgeObject{ID: other}},
		0.5, 5,
	)
	got, err := det.Detect(context.Background(), "ws", "x", &self)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("collisions = %d, want 1 (self excluded)", len(got))
	}
	if got[0].Similarity != 0.90 {
		t.Fatalf("kept the self-match: %+v", got)
	}
}

func TestDetectPropagatesEmbedError(t *testing.T) {
	det := NewCollisionDetector(
		&fakeEmbedder{err: errors.New("quota")},
		&fakeEmbeddingRepo{},
		&fakeHydrator{},
		0, 0,
	)
	if _, err := det.Detect(context.Background(), "ws", "x", nil); err == nil {
		t.Fatal("expected error from embedder")
	}
}

func TestDetectDefaultsThreshold(t *testing.T) {
	repo := &fakeEmbeddingRepo{similar: []ScoredSearchHit{
		hit(uuid.New().String(), 0.80), // above default 0.75
		hit(uuid.New().String(), 0.60), // below
	}}
	det := NewCollisionDetector(
		&fakeEmbedder{vec: []float32{0.1}},
		repo,
		&fakeHydrator{obj: &domain.KnowledgeObject{ID: uuid.New()}},
		0, 0, // threshold<=0 -> DefaultCollisionThreshold (0.75)
	)
	got, err := det.Detect(context.Background(), "ws", "x", nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("collisions = %d, want 1 with default threshold 0.75", len(got))
	}
}
