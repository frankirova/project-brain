package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeAPI records calls and returns canned responses.
type fakeAPI struct {
	searchWS, searchQ          string
	searchLimit                int
	collisionWS, collisionText string
	ingestWS, ingestContent    string
	ingestType, ingestTitle    string
	err                        error
}

func (f *fakeAPI) Search(_ context.Context, ws, q string, limit int) (json.RawMessage, error) {
	f.searchWS, f.searchQ, f.searchLimit = ws, q, limit
	return json.RawMessage(`{"results":[]}`), f.err
}

func (f *fakeAPI) CheckCollision(_ context.Context, ws, text string) (json.RawMessage, error) {
	f.collisionWS, f.collisionText = ws, text
	return json.RawMessage(`{"collisions":[]}`), f.err
}

func (f *fakeAPI) Ingest(_ context.Context, ws, content, objType, title string) (json.RawMessage, error) {
	f.ingestWS, f.ingestContent, f.ingestType, f.ingestTitle = ws, content, objType, title
	return json.RawMessage(`{"object_id":"x"}`), f.err
}

func toolByName(s *Server, name string) registeredTool {
	return s.tools[name]
}

func TestRegisterDefaultToolsRegistersThree(t *testing.T) {
	s := NewServer("x", "1", nil)
	RegisterDefaultTools(s, &fakeAPI{}, "default")
	for _, name := range []string{"search_knowledge", "check_collision", "save_knowledge"} {
		if _, ok := s.tools[name]; !ok {
			t.Errorf("missing tool %q", name)
		}
	}
	if len(s.order) != 3 {
		t.Errorf("order = %d, want 3", len(s.order))
	}
}

func TestSearchToolUsesDefaultWorkspace(t *testing.T) {
	api := &fakeAPI{}
	s := NewServer("x", "1", nil)
	RegisterDefaultTools(s, api, "demo-gemini")

	_, err := toolByName(s, "search_knowledge").handler(context.Background(), map[string]any{
		"query": "cobramos plata",
		"limit": float64(7),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if api.searchWS != "demo-gemini" {
		t.Errorf("workspace = %q, want default demo-gemini", api.searchWS)
	}
	if api.searchQ != "cobramos plata" || api.searchLimit != 7 {
		t.Errorf("got q=%q limit=%d", api.searchQ, api.searchLimit)
	}
}

func TestSearchToolRequiresQuery(t *testing.T) {
	s := NewServer("x", "1", nil)
	RegisterDefaultTools(s, &fakeAPI{}, "default")
	_, err := toolByName(s, "search_knowledge").handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error when query missing")
	}
}

func TestCollisionToolPassesContentAndWorkspace(t *testing.T) {
	api := &fakeAPI{}
	s := NewServer("x", "1", nil)
	RegisterDefaultTools(s, api, "default")
	_, err := toolByName(s, "check_collision").handler(context.Background(), map[string]any{
		"content":      "propongo Python",
		"workspace_id": "ws-9",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if api.collisionWS != "ws-9" || api.collisionText != "propongo Python" {
		t.Errorf("got ws=%q text=%q", api.collisionWS, api.collisionText)
	}
}

func TestSaveToolForwardsOptionalFields(t *testing.T) {
	api := &fakeAPI{}
	s := NewServer("x", "1", nil)
	RegisterDefaultTools(s, api, "default")
	_, err := toolByName(s, "save_knowledge").handler(context.Background(), map[string]any{
		"content": "usamos Redis",
		"type":    "decision",
		"title":   "Cache",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if api.ingestContent != "usamos Redis" || api.ingestType != "decision" || api.ingestTitle != "Cache" {
		t.Errorf("got %+v", api)
	}
}

func TestToolPropagatesAPIError(t *testing.T) {
	api := &fakeAPI{err: errors.New("api down")}
	s := NewServer("x", "1", nil)
	RegisterDefaultTools(s, api, "default")
	_, err := toolByName(s, "check_collision").handler(context.Background(), map[string]any{"content": "x"})
	if err == nil {
		t.Fatal("expected API error to propagate")
	}
}
