package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// roundTrip feeds one request line through Serve and returns the decoded
// response. It registers a single echo tool for tools/call coverage.
func newTestServer() *Server {
	s := NewServer("project-brain", "0.1.0", nil)
	s.AddTool("echo", "echoes back", objectSchema(map[string]any{
		"text": stringProp("text to echo"),
	}, []string{"text"}), func(_ context.Context, args map[string]any) (string, error) {
		return "echo:" + stringArg(args, "text", ""), nil
	})
	return s
}

func roundTrip(t *testing.T, s *Server, requestLine string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(requestLine+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if out.Len() == 0 {
		return nil // notification: no response
	}
	var resp map[string]any
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out.String(), err)
	}
	return resp
}

func TestInitializeReturnsServerInfo(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)

	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("no result: %+v", resp)
	}
	// We echo back the client's supported protocol version.
	if result["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocolVersion = %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info == nil || info["name"] != "project-brain" {
		t.Errorf("serverInfo = %+v", result["serverInfo"])
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities missing tools: %+v", caps)
	}
}

func TestInitializeDefaultsProtocolVersion(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want default %s", result["protocolVersion"], protocolVersion)
	}
}

func TestToolsListReturnsRegisteredTools(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "echo" {
		t.Errorf("tool name = %v", first["name"])
	}
	if _, ok := first["inputSchema"]; !ok {
		t.Errorf("tool missing inputSchema: %+v", first)
	}
}

func TestToolsCallExecutesHandler(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hola"}}}`)
	result := resp["result"].(map[string]any)
	content := result["content"].([]any)
	first := content[0].(map[string]any)
	if first["text"] != "echo:hola" {
		t.Errorf("content text = %v", first["text"])
	}
	if _, isErr := result["isError"]; isErr {
		t.Errorf("unexpected isError on success: %+v", result)
	}
}

func TestToolsCallUnknownToolIsError(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ghost","arguments":{}}}`)
	result := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("expected isError for unknown tool: %+v", result)
	}
}

func TestToolHandlerErrorBecomesToolError(t *testing.T) {
	s := NewServer("x", "1", nil)
	s.AddTool("boom", "always fails", objectSchema(nil, nil), func(_ context.Context, _ map[string]any) (string, error) {
		return "", errBoom
	})
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	result := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("expected isError, got %+v", result)
	}
	// A tool error must NOT surface as a transport-level JSON-RPC error.
	if _, ok := resp["error"]; ok {
		t.Errorf("tool error leaked into transport error: %+v", resp)
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp != nil {
		t.Errorf("notification should produce no response, got %+v", resp)
	}
}

func TestUnknownMethodReturnsError(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{"jsonrpc":"2.0","id":9,"method":"resources/list"}`)
	rpcErr, _ := resp["error"].(map[string]any)
	if rpcErr == nil {
		t.Fatalf("expected error, got %+v", resp)
	}
	if rpcErr["code"].(float64) != -32601 {
		t.Errorf("code = %v, want -32601", rpcErr["code"])
	}
}

func TestMalformedLineReturnsParseError(t *testing.T) {
	s := newTestServer()
	resp := roundTrip(t, s, `{not json`)
	rpcErr := resp["error"].(map[string]any)
	if rpcErr["code"].(float64) != -32700 {
		t.Errorf("code = %v, want -32700", rpcErr["code"])
	}
}

var errBoom = boomError("boom")

type boomError string

func (e boomError) Error() string { return string(e) }
