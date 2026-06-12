package postgres

import (
	"context"
	"hash/fnv"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// stubEmbedder is a deterministic Embedder for integration tests.
// It produces 1536-dimensional vectors without calling any external API.
// Two identical texts produce identical vectors; two different texts
// land at different positions in the vector space (via FNV hash).
type stubEmbedder struct{}

func (s *stubEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	h := fnv.New32a()
	h.Write([]byte(text))
	idx := int(h.Sum32() % 1536)
	vec := make([]float32, 1536)
	vec[idx] = 1.0
	return vec, nil
}

func (s *stubEmbedder) Dimensions() int { return 1536 }
func (s *stubEmbedder) Model() string   { return "stub-test-embedder" }

func mustEmbed(t *testing.T, embedder app.Embedder, text string) []float32 {
	t.Helper()

	vec, err := embedder.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("Embed(%q): %v", text, err)
	}
	return vec
}

// TestEmbeddingRepoUpsertAndFindSimilar verifies that Upsert persists
// a vector and FindSimilar returns the closest object by cosine distance.
func TestEmbeddingRepoUpsertAndFindSimilar(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	svc := app.NewIngestTextService(db)
	res, err := svc.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: workspaceID,
		Content:     "embedding integration test content",
		Object:      domain.ObjectInput{Type: "note"},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	embedder := &stubEmbedder{}
	vec := mustEmbed(t, embedder, "embedding integration test content")

	repo := NewEmbeddingRepo(db.pool)
	if err := repo.Upsert(context.Background(), domain.Embedding{
		ObjectID:    res.ObjectID,
		WorkspaceID: workspaceID,
		Model:       embedder.Model(),
		Dimensions:  embedder.Dimensions(),
		Vector:      vec,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	hits, err := repo.FindSimilar(context.Background(), workspaceID, vec, 5)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("FindSimilar returned %d hits, want 1", len(hits))
	}
	if hits[0].ObjectID != res.ObjectID.String() {
		t.Fatalf("FindSimilar hit ID = %s, want %s", hits[0].ObjectID, res.ObjectID)
	}
	if hits[0].Score < 0.99 {
		t.Fatalf("cosine score = %f, want ~1.0 for identical vectors", hits[0].Score)
	}
	if hits[0].MatchType != "vector" {
		t.Fatalf("MatchType = %q, want vector", hits[0].MatchType)
	}
}

// TestVectorRetrieverSearch verifies the full vectorRetriever stack:
// embed query → FindSimilar → hydrate via FTSRetriever.
func TestVectorRetrieverSearch(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	svc := app.NewIngestTextService(db)
	queryText := "vector retriever integration test"
	res, err := svc.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: workspaceID,
		Content:     queryText,
		Object:      domain.ObjectInput{Type: "note", Title: "Vector test object"},
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	embedder := &stubEmbedder{}
	vec := mustEmbed(t, embedder, queryText)
	embeddingRepo := NewEmbeddingRepo(db.pool)
	if err := embeddingRepo.Upsert(context.Background(), domain.Embedding{
		ObjectID:    res.ObjectID,
		WorkspaceID: workspaceID,
		Model:       embedder.Model(),
		Dimensions:  embedder.Dimensions(),
		Vector:      vec,
	}); err != nil {
		t.Fatalf("Upsert embedding: %v", err)
	}

	ftsRetriever := NewFTSRetriever(db.pool)
	retriever := NewVectorRetriever(embedder, embeddingRepo, ftsRetriever, 5)

	results, err := retriever.Search(context.Background(), app.SearchQuery{
		Text:        queryText,
		WorkspaceID: workspaceID,
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search returned %d results, want 1", len(results))
	}
	if results[0].Object.ID != res.ObjectID {
		t.Fatalf("result ID = %s, want %s", results[0].Object.ID, res.ObjectID)
	}
	if results[0].Object.Title != "Vector test object" {
		t.Fatalf("hydrated title = %q, want %q", results[0].Object.Title, "Vector test object")
	}
	if results[0].MatchType != "vector" {
		t.Fatalf("MatchType = %q, want vector", results[0].MatchType)
	}
}

// TestCompositeRetrieverHybridSearch verifies RRF fusion against real Postgres:
// an object that appears in both FTS and vector results should rank above
// objects that appear in only one.
func TestCompositeRetrieverHybridSearch(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	svc := app.NewIngestTextService(db)
	embedder := &stubEmbedder{}
	embeddingRepo := NewEmbeddingRepo(db.pool)
	queryText := "hybrid rrfusion composite retriever test"

	ingestAndEmbed := func(content string) uuid.UUID {
		t.Helper()
		res, err := svc.Ingest(context.Background(), domain.IngestTextRequest{
			WorkspaceID: workspaceID,
			Content:     content,
			Object:      domain.ObjectInput{Type: "note"},
		})
		if err != nil {
			t.Fatalf("Ingest(%q): %v", content, err)
		}
		vec := mustEmbed(t, embedder, content)
		if err := embeddingRepo.Upsert(context.Background(), domain.Embedding{
			ObjectID:    res.ObjectID,
			WorkspaceID: workspaceID,
			Model:       embedder.Model(),
			Dimensions:  embedder.Dimensions(),
			Vector:      vec,
		}); err != nil {
			t.Fatalf("Upsert embedding for %q: %v", content, err)
		}
		return res.ObjectID
	}

	// objectA: exact query text → both FTS and vector will match it.
	objectA := ingestAndEmbed(queryText)

	// objectB: FTS-only; its text shares some tokens with the query but
	// its embedding vector is different (different hash position), so
	// vector search won't rank it highly. No embedding needed; FTS alone
	// puts it in the composite result set.
	resB, err := svc.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: workspaceID,
		Content:     "hybrid composite retriever additional document",
		Object:      domain.ObjectInput{Type: "note"},
	})
	if err != nil {
		t.Fatalf("Ingest objectB: %v", err)
	}
	objectB := resB.ObjectID

	ftsRetriever := NewFTSRetriever(db.pool)
	vRetriever := NewVectorRetriever(embedder, embeddingRepo, ftsRetriever, 10)
	composite := app.NewCompositeRetriever([]app.Retriever{ftsRetriever, vRetriever}, 60, 10)
	composite.SetHydrator(ftsRetriever)

	results, err := composite.Search(context.Background(), app.SearchQuery{
		Text:        queryText,
		WorkspaceID: workspaceID,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("composite Search: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("composite Search returned 0 results")
	}

	// objectA must appear in the results (it matches both retrievers).
	ids := make(map[uuid.UUID]bool, len(results))
	for _, r := range results {
		ids[r.Object.ID] = true
	}
	if !ids[objectA] {
		t.Errorf("objectA (%s) missing from composite results", objectA)
	}

	if !ids[objectB] {
		t.Fatalf("objectB (%s) missing from composite results; RRF comparison not exercisable", objectB)
	}

	// objectA must rank above objectB because RRF gives it score from
	// two retrievers vs objectB's single FTS contribution.
	var rankA, rankB int
	for i, r := range results {
		switch r.Object.ID {
		case objectA:
			rankA = i
		case objectB:
			rankB = i
		}
	}
	if rankA > rankB {
		t.Errorf("objectA at rank %d, objectB at rank %d; objectA should rank higher (dual-retriever RRF boost)", rankA, rankB)
	}
}
