package gemini

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbedSendsRequestAndParsesVector verifies the adapter speaks the
// Gemini embedContent contract: POST to /models/{model}:embedContent,
// the API key in the x-goog-api-key header (never the URL), the text in
// the request body, and a normalized vector parsed from the response.
func TestEmbedSendsRequestAndParsesVector(t *testing.T) {
	var gotPath, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"embedding":{"values":[3.0,4.0]}}`)
	}))
	defer srv.Close()

	e := NewEmbedder("test-key",
		WithBaseURL(srv.URL),
		WithModel("gemini-embedding-001"),
		WithDimensions(2),
	)

	vec, err := e.Embed(context.Background(), "hola mundo")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(vec) != 2 {
		t.Fatalf("len(vec)=%d, want 2", len(vec))
	}
	// [3,4] normalized by L2 norm 5 -> [0.6, 0.8].
	if math.Abs(float64(vec[0])-0.6) > 1e-6 || math.Abs(float64(vec[1])-0.8) > 1e-6 {
		t.Fatalf("vector not L2-normalized: got %v, want [0.6 0.8]", vec)
	}
	if gotKey != "test-key" {
		t.Errorf("x-goog-api-key header = %q, want %q", gotKey, "test-key")
	}
	if !strings.Contains(gotPath, "gemini-embedding-001:embedContent") {
		t.Errorf("path = %q, want it to contain %q", gotPath, "gemini-embedding-001:embedContent")
	}
	if !strings.Contains(gotBody, "hola mundo") {
		t.Errorf("body = %q, want it to contain the input text", gotBody)
	}
	if !strings.Contains(gotBody, "outputDimensionality") {
		t.Errorf("body = %q, want it to request outputDimensionality", gotBody)
	}
}

// TestEmbedRejectsEmptyText guards against wasting an API call (and a
// quota hit) on blank input.
func TestEmbedRejectsEmptyText(t *testing.T) {
	e := NewEmbedder("k")
	if _, err := e.Embed(context.Background(), "   "); err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
}

// TestEmbedErrorsOnNon200 verifies a non-200 (e.g. 429 quota) surfaces
// as an error instead of a silently empty vector.
func TestEmbedErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota exceeded"}}`)
	}))
	defer srv.Close()

	e := NewEmbedder("k", WithBaseURL(srv.URL))
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected error on HTTP 429, got nil")
	}
}

// TestMetadataAccessors verifies Model() and Dimensions() report what the
// repository needs to validate before insert.
func TestMetadataAccessors(t *testing.T) {
	e := NewEmbedder("k", WithModel("custom-model"), WithDimensions(1536))
	if e.Model() != "custom-model" {
		t.Errorf("Model() = %q, want %q", e.Model(), "custom-model")
	}
	if e.Dimensions() != 1536 {
		t.Errorf("Dimensions() = %d, want 1536", e.Dimensions())
	}
}

// TestDefaultsMatchEmbeddingsColumn locks the defaults to the existing
// vector(1536) column so a fresh NewEmbedder is insert-compatible.
func TestDefaultsMatchEmbeddingsColumn(t *testing.T) {
	e := NewEmbedder("k")
	if e.Dimensions() != 1536 {
		t.Errorf("default Dimensions() = %d, want 1536 (vector(1536) column)", e.Dimensions())
	}
	if e.Model() != DefaultModel {
		t.Errorf("default Model() = %q, want %q", e.Model(), DefaultModel)
	}
}
