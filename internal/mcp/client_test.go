package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
)

func TestClientSearchBuildsRequest(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"results":[],"count":0}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "secret")
	raw, err := c.Search(context.Background(), "ws-1", "motor", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(gotPath, "workspace_id=ws-1") || !strings.Contains(gotPath, "q=motor") || !strings.Contains(gotPath, "limit=5") {
		t.Errorf("unexpected query: %s", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if !json.Valid(raw) {
		t.Errorf("response not valid JSON: %s", raw)
	}
}

func TestClientCheckCollisionPostsBody(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		_, _ = w.Write([]byte(`{"collisions":[],"count":0}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if _, err := c.CheckCollision(context.Background(), "ws-1", "propongo Python"); err != nil {
		t.Fatalf("CheckCollision: %v", err)
	}
	if body["workspace_id"] != "ws-1" || body["content"] != "propongo Python" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestClientIngestPostsStructuredBody(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"object_id":"x"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if _, err := c.Ingest(context.Background(), "ws-1", "usamos Redis", "decision", "Cache"); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if body["workspace_id"] != "ws-1" || body["content"] != "usamos Redis" {
		t.Fatalf("unexpected body: %+v", body)
	}
	obj, ok := body["object"].(map[string]any)
	if !ok || obj["type"] != "decision" || obj["title"] != "Cache" {
		t.Errorf("unexpected object: %+v", body["object"])
	}
}

func TestClientNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"workspace_id is required"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.CheckCollision(context.Background(), "", "x")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestClientGetSddDocument200(t *testing.T) {
	const markdown = "# SDD Document — ws-1\n\n## Context\n\n_(none)_\n"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(markdown))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	got, err := c.GetSddDocument(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("GetSddDocument: %v", err)
	}
	if got != markdown {
		t.Errorf("body = %q, want %q", got, markdown)
	}
	if !strings.Contains(gotPath, "workspace_id=ws-1") {
		t.Errorf("path missing workspace_id; got %s", gotPath)
	}
}

func TestClientGetSddDocument404ReturnsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"NOT_FOUND","message":"no SDD document found for workspace missing"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.GetSddDocument(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !errors.Is(err, app.ErrNotFound) {
		t.Errorf("want app.ErrNotFound, got %v", err)
	}
}
