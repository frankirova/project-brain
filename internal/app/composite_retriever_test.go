package app

import (
	"context"
	"errors"
	"testing"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

type fakeRet struct {
	name  string
	hits  []SearchResult
	err   error
	calls int
}

func (f *fakeRet) Search(_ context.Context, _ SearchQuery) ([]SearchResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func TestCompositeSingleRetriever(t *testing.T) {
	fts := &fakeRet{
		name: "fts",
		hits: []SearchResult{
			{Object: objectFromID("11111111-1111-1111-1111-111111111111"), Score: 0.9, MatchType: "fts"},
		},
	}
	c := NewCompositeRetriever([]Retriever{fts}, 60, 10)
	results, err := c.Search(context.Background(), SearchQuery{Text: "x", WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].MatchType != "fts" {
		t.Fatalf("matchType = %q, want fts", results[0].MatchType)
	}
}

func TestCompositeMergesWithRRF(t *testing.T) {
	// Same object ID appears in both retrievers; rank differs.
	// RRF gives the object a higher combined score.
	commonID := "11111111-1111-1111-1111-111111111111"
	ftsOnly := "22222222-2222-2222-2222-222222222222"
	vecOnly := "33333333-3333-3333-3333-333333333333"

	fts := &fakeRet{
		name: "fts",
		hits: []SearchResult{
			{Object: objectFromID(commonID), Score: 0.95, MatchType: "fts"},
			{Object: objectFromID(ftsOnly), Score: 0.70, MatchType: "fts"},
		},
	}
	vec := &fakeRet{
		name: "vector",
		hits: []SearchResult{
			{Object: objectFromID(commonID), Score: 0.85, MatchType: "vector"},
			{Object: objectFromID(vecOnly), Score: 0.60, MatchType: "vector"},
		},
	}

	c := NewCompositeRetriever([]Retriever{fts, vec}, 60, 10)
	results, err := c.Search(context.Background(), SearchQuery{Text: "x", WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	// The common object should rank first because it appears in
	// both retrievers; the RRF sum is 1/61 + 1/61 = ~0.0328.
	// Each retriever-only object gets 1/61 = ~0.0164.
	if results[0].Object.ID.String() != commonID {
		t.Fatalf("top result = %s, want commonID %s (should rank first via RRF)", results[0].Object.ID, commonID)
	}
}

func TestCompositeLimit(t *testing.T) {
	fts := &fakeRet{
		name: "fts",
		hits: []SearchResult{
			{Object: objectFromID("11111111-1111-1111-1111-111111111111"), Score: 0.9, MatchType: "fts"},
			{Object: objectFromID("22222222-2222-2222-2222-222222222222"), Score: 0.8, MatchType: "fts"},
			{Object: objectFromID("33333333-3333-3333-3333-333333333333"), Score: 0.7, MatchType: "fts"},
		},
	}
	c := NewCompositeRetriever([]Retriever{fts}, 60, 2)
	results, err := c.Search(context.Background(), SearchQuery{Text: "x", WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (limit)", len(results))
	}
}

func TestCompositePropagatesError(t *testing.T) {
	fts := &fakeRet{
		name: "fts",
		err:  errors.New("fts broken"),
	}
	c := NewCompositeRetriever([]Retriever{fts}, 60, 10)
	_, err := c.Search(context.Background(), SearchQuery{Text: "x", WorkspaceID: "ws"})
	if err == nil {
		t.Fatal("expected error when only retriever fails")
	}
}

func TestCompositeEmptyPrimaryList(t *testing.T) {
	c := NewCompositeRetriever(nil, 60, 10)
	results, err := c.Search(context.Background(), SearchQuery{Text: "x", WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("empty primaries should not error: %v", err)
	}
	if results != nil {
		t.Fatalf("results = %v, want nil", results)
	}
}

type fakeHydrator struct {
	obj *domain.KnowledgeObject
	err error
}

func (f *fakeHydrator) FindByID(_ context.Context, _ string, _ uuid.UUID) (*domain.KnowledgeObject, error) {
	return f.obj, f.err
}

func TestCompositeHydratesResults(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	hydrated := &domain.KnowledgeObject{
		ID:      id,
		Title:   "Real title",
		Content: "real content",
		Status:  domain.KnowledgeObjectStatusValidated,
	}

	fts := &fakeRet{
		hits: []SearchResult{
			{Object: objectFromID(id.String()), Score: 0.9, MatchType: "fts"},
		},
	}
	c := NewCompositeRetriever([]Retriever{fts}, 60, 10)
	c.SetHydrator(&fakeHydrator{obj: hydrated})

	results, err := c.Search(context.Background(), SearchQuery{Text: "x", WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Object.Title != "Real title" {
		t.Fatalf("hydrated title = %q, want %q", results[0].Object.Title, "Real title")
	}
	if results[0].Object.Status != domain.KnowledgeObjectStatusValidated {
		t.Fatalf("hydrated status = %q, want %q", results[0].Object.Status, domain.KnowledgeObjectStatusValidated)
	}
}

func TestCompositeHydrationFallback(t *testing.T) {
	// Hydrator returns an error (e.g., object deleted between
	// embedding scan and hydration). The composite should fall
	// back to a stub with just the ID rather than failing the
	// whole search.
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	fts := &fakeRet{
		hits: []SearchResult{
			{Object: objectFromID(id.String()), Score: 0.9, MatchType: "fts"},
		},
	}
	c := NewCompositeRetriever([]Retriever{fts}, 60, 10)
	c.SetHydrator(&fakeHydrator{err: errors.New("not found")})

	results, err := c.Search(context.Background(), SearchQuery{Text: "x", WorkspaceID: "ws"})
	if err != nil {
		t.Fatalf("Search should not error on hydrator failure: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (fallback stub)", len(results))
	}
	// Stub has just the ID, no content.
	if results[0].Object.ID != id {
		t.Fatalf("fallback ID = %v, want %v", results[0].Object.ID, id)
	}
	if results[0].Object.Title != "" {
		t.Fatalf("fallback title = %q, want empty", results[0].Object.Title)
	}
}
