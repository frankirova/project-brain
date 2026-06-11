// Package gemini provides a Google Gemini-backed implementation of the
// app.Embedder port. It calls the Generative Language API's
// embedContent endpoint over HTTP and returns L2-normalized vectors.
//
// The default model is gemini-embedding-001 with an output
// dimensionality of 1536, chosen to match the embeddings table column
// (vector(1536), migrations/0007_embeddings.sql). gemini-embedding-001
// supports a configurable output size, so we ask for exactly 1536
// instead of altering the schema.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
)

// Compile-time guarantee that *Embedder satisfies the app.Embedder port.
var _ app.Embedder = (*Embedder)(nil)

const (
	// DefaultModel is the GA Gemini embedding model. It supports a
	// configurable output dimensionality.
	DefaultModel = "gemini-embedding-001"
	// DefaultDimensions matches the vector(1536) embeddings column.
	DefaultDimensions = 1536

	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	defaultTimeout = 30 * time.Second
	maxRespBytes   = 1 << 20 // 1 MiB cap on the response body we read.
)

// Embedder calls Gemini's embedContent endpoint to turn text into a
// vector. It is safe for concurrent use: the http.Client is shared and
// no per-call state lives on the struct.
type Embedder struct {
	apiKey     string
	model      string
	dimensions int
	baseURL    string
	httpClient *http.Client
}

// Option configures an Embedder at construction time.
type Option func(*Embedder)

// WithBaseURL overrides the API base URL. Used by tests to point at an
// httptest.Server.
func WithBaseURL(u string) Option { return func(e *Embedder) { e.baseURL = u } }

// WithModel overrides the embedding model identifier.
func WithModel(m string) Option { return func(e *Embedder) { e.model = m } }

// WithDimensions overrides the requested output dimensionality. It must
// match the embeddings table column to be insert-compatible.
func WithDimensions(d int) Option { return func(e *Embedder) { e.dimensions = d } }

// WithHTTPClient injects a custom http.Client (timeouts, transport).
func WithHTTPClient(c *http.Client) Option { return func(e *Embedder) { e.httpClient = c } }

// NewEmbedder builds an Embedder with the given API key and options.
// Defaults: gemini-embedding-001, 1536 dimensions, a 30s timeout.
func NewEmbedder(apiKey string, opts ...Option) *Embedder {
	e := &Embedder{
		apiKey:     apiKey,
		model:      DefaultModel,
		dimensions: DefaultDimensions,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Dimensions reports the output vector size this embedder produces.
func (e *Embedder) Dimensions() int { return e.dimensions }

// Model reports the identifier persisted in embeddings.model.
func (e *Embedder) Model() string { return e.model }

type embedRequest struct {
	Model                string       `json:"model"`
	Content              embedContent `json:"content"`
	OutputDimensionality int          `json:"outputDimensionality,omitempty"`
}

type embedContent struct {
	Parts []embedPart `json:"parts"`
}

type embedPart struct {
	Text string `json:"text"`
}

type embedResponse struct {
	Embedding struct {
		Values []float32 `json:"values"`
	} `json:"embedding"`
}

// Embed turns text into an L2-normalized vector of e.Dimensions()
// elements. Empty text is rejected before any network call.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("gemini: empty text")
	}

	payload := embedRequest{
		Model:                "models/" + e.model,
		Content:              embedContent{Parts: []embedPart{{Text: text}}},
		OutputDimensionality: e.dimensions,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/models/%s:embedContent", e.baseURL, e.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("gemini: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Key goes in a header, not the URL, so it never lands in access logs.
	req.Header.Set("x-goog-api-key", e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBytes))
	if err != nil {
		return nil, fmt.Errorf("gemini: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed embedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("gemini: decode response: %w", err)
	}
	if len(parsed.Embedding.Values) == 0 {
		return nil, errors.New("gemini: response contained no embedding values")
	}

	return normalize(parsed.Embedding.Values), nil
}

// normalize scales v to unit L2 length. gemini-embedding-001 does not
// return normalized vectors when a reduced output dimensionality is
// requested, so we normalize here; a zero vector is returned unchanged.
func normalize(v []float32) []float32 {
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	if sumSq == 0 {
		return v
	}
	inv := 1.0 / math.Sqrt(sumSq)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}
