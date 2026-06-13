// Package mcp exposes the project-brain knowledge API to MCP-capable
// agents (Claude, Hermes, etc.) over a stdio JSON-RPC server. It is a
// thin adapter: every tool forwards to the running HTTP API, so the
// agent needs only the API URL and auth token — never the database or
// embedding-provider credentials.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
)

// Client is a minimal HTTP client for the project-brain API. Each call
// returns the raw JSON response body so tools can forward it verbatim
// to the agent.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a client for the API at baseURL. token, when
// non-empty, is sent as a Bearer credential on every request.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Search runs a hybrid semantic search. limit<=0 lets the API default it.
func (c *Client) Search(ctx context.Context, workspaceID, query string, limit int) (json.RawMessage, error) {
	q := url.Values{}
	q.Set("workspace_id", workspaceID)
	q.Set("q", query)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return c.do(ctx, http.MethodGet, "/v1/search?"+q.Encode(), nil)
}

// CheckCollision returns existing knowledge that semantically collides
// with content, without storing anything.
func (c *Client) CheckCollision(ctx context.Context, workspaceID, content string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/v1/check-collision", map[string]string{
		"workspace_id": workspaceID,
		"content":      content,
	})
}

// Ingest stores a new knowledge object. objType and title are optional;
// empty objType lets the API default it.
func (c *Client) Ingest(ctx context.Context, workspaceID, content, objType, title string) (json.RawMessage, error) {
	body := map[string]any{
		"workspace_id": workspaceID,
		"content":      content,
		"source":       map[string]any{"type": "mcp"},
		"object": map[string]any{
			"type":       objType,
			"title":      title,
			"created_by": "mcp-agent",
		},
	}
	return c.do(ctx, http.MethodPost, "/v1/ingest-text", body)
}

// GetSddDocument retrieves the workspace SDD document as a Markdown string.
// It returns app.ErrNotFound when the API responds with 404.
func (c *Client) GetSddDocument(ctx context.Context, workspaceID string) (string, error) {
	q := url.Values{}
	q.Set("workspace_id", workspaceID)
	path := "/v1/sdd-document?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return "", app.ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("API GET %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}

	return string(data), nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.RawMessage(data), nil
}
