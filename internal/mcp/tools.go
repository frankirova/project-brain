package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// knowledgeAPI is the slice of the project-brain API the tools need.
// *Client satisfies it; tests inject a fake.
type knowledgeAPI interface {
	Search(ctx context.Context, workspaceID, query string, limit int) (json.RawMessage, error)
	CheckCollision(ctx context.Context, workspaceID, content string) (json.RawMessage, error)
	Ingest(ctx context.Context, workspaceID, content, objType, title string) (json.RawMessage, error)
}

// RegisterDefaultTools wires the three knowledge tools onto s, backed by
// api. defaultWorkspace is used whenever a call omits workspace_id.
func RegisterDefaultTools(s *Server, api knowledgeAPI, defaultWorkspace string) {
	s.AddTool(
		"search_knowledge",
		"Search the team's knowledge base by meaning (not just keywords). "+
			"Use this to recall prior decisions, facts, or context before answering.",
		objectSchema(
			map[string]any{
				"query":        stringProp("What to search for, in natural language."),
				"workspace_id": stringProp("Tenant/workspace scope. Defaults to '" + defaultWorkspace + "'."),
				"limit":        intProp("Max results to return (default 10)."),
			},
			[]string{"query"},
		),
		func(ctx context.Context, args map[string]any) (string, error) {
			query := stringArg(args, "query", "")
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			raw, err := api.Search(ctx, stringArg(args, "workspace_id", defaultWorkspace), query, intArg(args, "limit", 0))
			if err != nil {
				return "", err
			}
			return string(raw), nil
		},
	)

	s.AddTool(
		"check_collision",
		"Check whether a statement, decision, or claim would collide with "+
			"existing knowledge BEFORE asserting it. Returns conflicting items "+
			"with a verdict (duplicate / strong_overlap / related). Use this to "+
			"avoid contradicting decisions the team already made.",
		objectSchema(
			map[string]any{
				"content":      stringProp("The statement or decision to check for collisions."),
				"workspace_id": stringProp("Tenant/workspace scope. Defaults to '" + defaultWorkspace + "'."),
			},
			[]string{"content"},
		),
		func(ctx context.Context, args map[string]any) (string, error) {
			content := stringArg(args, "content", "")
			if content == "" {
				return "", fmt.Errorf("content is required")
			}
			raw, err := api.CheckCollision(ctx, stringArg(args, "workspace_id", defaultWorkspace), content)
			if err != nil {
				return "", err
			}
			return string(raw), nil
		},
	)

	s.AddTool(
		"save_knowledge",
		"Persist a new fact, decision, or note into the team's knowledge base "+
			"so it can be recalled and collision-checked later.",
		objectSchema(
			map[string]any{
				"content":      stringProp("The knowledge to store."),
				"workspace_id": stringProp("Tenant/workspace scope. Defaults to '" + defaultWorkspace + "'."),
				"type":         stringProp("Object type, e.g. 'decision', 'fact', 'document'. Optional."),
				"title":        stringProp("Short title. Optional."),
			},
			[]string{"content"},
		),
		func(ctx context.Context, args map[string]any) (string, error) {
			content := stringArg(args, "content", "")
			if content == "" {
				return "", fmt.Errorf("content is required")
			}
			raw, err := api.Ingest(ctx,
				stringArg(args, "workspace_id", defaultWorkspace),
				content,
				stringArg(args, "type", ""),
				stringArg(args, "title", ""),
			)
			if err != nil {
				return "", err
			}
			return string(raw), nil
		},
	)
}

// --- JSON Schema + argument helpers ---

func objectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func stringArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64: // JSON numbers decode to float64
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}
