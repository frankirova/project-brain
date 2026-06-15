package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/frankirova/project-brain/internal/config"
)

// NewLogger returns a slog.Logger configured per environment and
// PROJECT_BRAIN_LOG_LEVEL override. Production logs are JSON for
// aggregation; development logs are text for readability.
//
// The shape and behavior of this function is preserved byte-for-byte
// from the original cmd/api/main.go helper so the change-19 refactor
// cannot drift the log format.
func NewLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}
	if v := os.Getenv("PROJECT_BRAIN_LOG_LEVEL"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			level = slog.LevelDebug
		case "info":
			level = slog.LevelInfo
		case "warn", "warning":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		default:
			fmt.Fprintf(os.Stderr, "unknown PROJECT_BRAIN_LOG_LEVEL=%q, falling back to default\n", v)
		}
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if env == "production" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// EnforceInMemoryProductionGuard refuses to start when the in-memory
// UoW would be selected (cfg.DatabaseDSN == "") in a production
// environment. The caller (main) MUST invoke this BEFORE any
// Build* call so a misconfigured production deploy never reaches
// the service-construction path.
//
// Returns a non-nil error and emits a structured error log line on
// refusal; returns nil otherwise. The error message is the
// pre-refactor wording so log scrapers and on-call runbooks stay in
// sync with the operator-facing signal.
func EnforceInMemoryProductionGuard(cfg config.Config, logger *slog.Logger) error {
	// Postgres DSN is the only signal we need: when it is set, the
	// postgres branch of BuildServices wins and the in-memory
	// refusal does not apply. Mirrors the cfg.DatabaseDSN != "" check
	// in BuildServices.
	if cfg.DatabaseDSN != "" {
		return nil
	}
	if cfg.Environment != "production" {
		return nil
	}
	logger.Error("in-memory uow refused in production",
		slog.String("reason", "PROJECT_BRAIN_DATABASE_DSN unset"))
	return errors.New("in-memory uow refused in production")
}
