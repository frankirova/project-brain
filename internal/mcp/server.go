package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
)

// protocolVersion is the MCP revision this server speaks. When a client
// announces a different (supported) version on initialize, we echo it
// back; otherwise we offer this one.
const protocolVersion = "2024-11-05"

// ToolHandler executes a tool call. args is the decoded "arguments"
// object from the request. The returned string becomes the tool's text
// result; a non-nil error is reported to the agent as a tool error
// (isError), never as a transport-level failure.
type ToolHandler func(ctx context.Context, args map[string]any) (string, error)

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type registeredTool struct {
	def     toolDef
	handler ToolHandler
}

// Server is a stdio JSON-RPC 2.0 MCP server. Messages are newline
// delimited: one JSON object per line in, one per line out. Logs go to
// stderr so they never corrupt the protocol stream on stdout.
type Server struct {
	name    string
	version string
	order   []string
	tools   map[string]registeredTool
	log     *slog.Logger
}

// NewServer builds a server identified by name/version. logger may be
// nil (falls back to slog.Default()); callers should ensure it writes to
// stderr, not stdout.
func NewServer(name, version string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		name:    name,
		version: version,
		tools:   make(map[string]registeredTool),
		log:     logger,
	}
}

// AddTool registers a callable tool. schema is the JSON Schema object
// describing the tool's arguments.
func (s *Server) AddTool(name, description string, schema map[string]any, h ToolHandler) {
	if _, exists := s.tools[name]; !exists {
		s.order = append(s.order, name)
	}
	s.tools[name] = registeredTool{
		def:     toolDef{Name: name, Description: description, InputSchema: schema},
		handler: h,
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests from in and writes responses to out until in is
// exhausted (EOF) or ctx is cancelled. Notifications (requests without
// an id) are processed but produce no response.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	// Allow large lines: tool arguments can carry sizable text payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if encErr := enc.Encode(parseErrorResponse()); encErr != nil {
				return encErr
			}
			continue
		}

		resp, respond := s.handle(ctx, req)
		if !respond {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		return s.ok(req.ID, s.initializeResult(req.Params)), true
	case "notifications/initialized":
		return rpcResponse{}, false
	case "ping":
		return s.ok(req.ID, map[string]any{}), true
	case "tools/list":
		return s.ok(req.ID, map[string]any{"tools": s.toolList()}), true
	case "tools/call":
		return s.callTool(ctx, req), true
	default:
		if isNotification {
			return rpcResponse{}, false
		}
		return s.fail(req.ID, -32601, "method not found: "+req.Method), true
	}
}

func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	proto := protocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
			proto = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": proto,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
	}
}

func (s *Server) toolList() []toolDef {
	list := make([]toolDef, 0, len(s.order))
	for _, name := range s.order {
		list = append(list, s.tools[name].def)
	}
	return list
}

func (s *Server) callTool(ctx context.Context, req rpcRequest) rpcResponse {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return s.fail(req.ID, -32602, "invalid tools/call params")
	}

	tool, ok := s.tools[p.Name]
	if !ok {
		return s.ok(req.ID, errorContent("unknown tool: "+p.Name))
	}

	out, err := tool.handler(ctx, p.Arguments)
	if err != nil {
		s.log.Error("tool call failed", slog.String("tool", p.Name), slog.String("error", err.Error()))
		return s.ok(req.ID, errorContent(err.Error()))
	}
	return s.ok(req.ID, textContent(out))
}

func textContent(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

func errorContent(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": true,
	}
}

func (s *Server) ok(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) fail(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func parseErrorResponse() rpcResponse {
	return rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("null"),
		Error:   &rpcError{Code: -32700, Message: "parse error"},
	}
}
