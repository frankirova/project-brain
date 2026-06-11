package app

import (
	"context"
	"errors"
	"testing"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

type fakeEmbedder struct {
	vec  []float32
	err  error
	seen string
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.seen = text
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

func (f *fakeEmbedder) Dimensions() int { return len(f.vec) }
func (f *fakeEmbedder) Model() string   { return "fake-model" }

type fakeEmbeddingRepo struct {
	upserted []domain.Embedding
	err      error
	// similar and findErr drive FindSimilar (used by collision tests).
	similar []ScoredSearchHit
	findErr error
}

func (r *fakeEmbeddingRepo) Upsert(_ context.Context, emb domain.Embedding) error {
	if r.err != nil {
		return r.err
	}
	r.upserted = append(r.upserted, emb)
	return nil
}

func (r *fakeEmbeddingRepo) FindSimilar(_ context.Context, _ string, _ []float32, _ int) ([]ScoredSearchHit, error) {
	return r.similar, r.findErr
}

func TestEmbeddingHookUpsertsVectorWithMetadata(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	repo := &fakeEmbeddingRepo{}
	hook := NewEmbeddingHook(emb, repo)

	id := uuid.New()
	err := hook(context.Background(), domain.KnowledgeObject{
		ID:          id,
		WorkspaceID: "ws-1",
		Title:       "T",
		Summary:     "S",
		Content:     "C",
	})
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}

	if len(repo.upserted) != 1 {
		t.Fatalf("upserts = %d, want 1", len(repo.upserted))
	}
	got := repo.upserted[0]
	if got.ObjectID != id || got.WorkspaceID != "ws-1" {
		t.Errorf("identity wrong: %+v", got)
	}
	if got.Model != "fake-model" || got.Dimensions != 3 {
		t.Errorf("model/dims wrong: model=%q dims=%d", got.Model, got.Dimensions)
	}
	if len(got.Vector) != 3 {
		t.Errorf("vector len = %d, want 3", len(got.Vector))
	}
	// Title, summary, and content are joined for richer similarity signal.
	if emb.seen != "T\nS\nC" {
		t.Errorf("embedded text = %q, want %q", emb.seen, "T\nS\nC")
	}
}

func TestEmbeddingHookPropagatesEmbedError(t *testing.T) {
	hook := NewEmbeddingHook(&fakeEmbedder{err: errors.New("quota exceeded")}, &fakeEmbeddingRepo{})
	err := hook(context.Background(), domain.KnowledgeObject{ID: uuid.New(), Content: "x"})
	if err == nil {
		t.Fatal("expected error from embedder, got nil")
	}
}

func TestEmbeddingHookSkipsEmptyText(t *testing.T) {
	emb := &fakeEmbedder{vec: []float32{1}}
	repo := &fakeEmbeddingRepo{}
	hook := NewEmbeddingHook(emb, repo)

	err := hook(context.Background(), domain.KnowledgeObject{ID: uuid.New()})
	if err != nil {
		t.Fatalf("hook returned error on empty text: %v", err)
	}
	if len(repo.upserted) != 0 {
		t.Fatal("upserted an embedding for empty-text object, want skip")
	}
}
