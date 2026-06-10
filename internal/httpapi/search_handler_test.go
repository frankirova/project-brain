package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

type fakeRetriever struct {
	results []app.SearchResult
	err     error
}

func (f *fakeRetriever) Search(_ context.Context, q app.SearchQuery) ([]app.SearchResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func TestSearchHandler_MissingWorkspaceID(t *testing.T) {
	h := NewSearchHandler(&fakeRetriever{})
	req := httptest.NewRequest("GET", "/v1/search?q=foo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestSearchHandler_EmptyQuery(t *testing.T) {
	h := NewSearchHandler(&fakeRetriever{})
	req := httptest.NewRequest("GET", "/v1/search?workspace_id=ws-1", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	var body searchResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 0 || len(body.Results) != 0 {
		t.Fatalf("empty query: count=%d results=%d, want 0/0", body.Count, len(body.Results))
	}
}

func TestSearchHandler_Results(t *testing.T) {
	results := []app.SearchResult{
		{
			Object:    domain.KnowledgeObject{ID: mustUUID("11111111-1111-1111-1111-111111111111"), Content: "match"},
			Score:     0.95,
			MatchType: "fts",
		},
		{
			Object:    domain.KnowledgeObject{ID: mustUUID("22222222-2222-2222-2222-222222222222"), Content: "weak"},
			Score:     0.40,
			MatchType: "fts",
		},
	}
	h := NewSearchHandler(&fakeRetriever{results: results})
	req := httptest.NewRequest("GET", "/v1/search?q=foo&workspace_id=ws-1", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	var body searchResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 2 {
		t.Fatalf("count = %d, want 2", body.Count)
	}
	if body.Query != "foo" {
		t.Fatalf("query = %q, want %q", body.Query, "foo")
	}
	if body.Results[0].Score != 0.95 {
		t.Fatalf("first result score = %f, want 0.95", body.Results[0].Score)
	}
}

func TestSearchHandler_InvalidLimit(t *testing.T) {
	h := NewSearchHandler(&fakeRetriever{})
	req := httptest.NewRequest("GET", "/v1/search?workspace_id=ws-1&limit=abc", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestSearchHandler_RetrieverError(t *testing.T) {
	h := NewSearchHandler(&fakeRetriever{err: errFake})
	req := httptest.NewRequest("GET", "/v1/search?q=x&workspace_id=ws-1", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rr.Code)
	}
}

var errFake = errFakeSentinel{}

type errFakeSentinel struct{}

func (errFakeSentinel) Error() string { return "fake retriever failure" }

func mustUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}
