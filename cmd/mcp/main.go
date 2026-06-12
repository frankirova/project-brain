// Command mcp runs a stdio MCP server that exposes the project-brain
// knowledge API (search, collision check, save) as tools for MCP-capable
// agents such as Hermes or Claude.
//
// The agent launches this binary as a subprocess and speaks JSON-RPC
// over stdin/stdout. The server forwards every tool call to the running
// project-brain HTTP API, so it needs only:
//
//	PROJECT_BRAIN_API_URL    base URL of the API (default http://localhost:8050)
//	PROJECT_BRAIN_AUTH_TOKEN bearer token, if the API has auth enabled
//	PROJECT_BRAIN_MCP_WORKSPACE default workspace_id (default "default")
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/frankirova/project-brain/internal/mcp"
)

func main() {
	// Logs MUST go to stderr: stdout is the JSON-RPC channel.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	apiURL := getenv("PROJECT_BRAIN_API_URL", "http://localhost:8050")
	token := os.Getenv("PROJECT_BRAIN_AUTH_TOKEN")
	workspace := getenv("PROJECT_BRAIN_MCP_WORKSPACE", "default")

	client := mcp.NewClient(apiURL, token)
	server := mcp.NewServer("project-brain", "0.1.0", logger)
	mcp.RegisterDefaultTools(server, client, workspace)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("mcp server starting",
		slog.String("transport", "stdio"),
		slog.String("api_url", apiURL),
		slog.String("workspace", workspace),
		slog.Bool("auth", token != ""))

	if err := server.Serve(ctx, os.Stdin, os.Stdout); err != nil && err != context.Canceled {
		logger.Error("mcp server stopped", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
