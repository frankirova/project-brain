package main

import (
	"fmt"
	"log/slog"

	"github.com/frankirova/project-brain/internal/config"
)

// Environment variable names used by the production-auth invariant. Kept as
// constants so the structured log entry and the error message stay in sync
// with the names documented for operators.
const (
	envVarName         = "PROJECT_BRAIN_ENV"
	authTokenVarName   = "PROJECT_BRAIN_AUTH_TOKEN"
	productionEnvValue = "production"
)

// enforceProductionAuth is the fail-closed startup invariant introduced in
// change-16 PR 1.
//
// When PROJECT_BRAIN_ENV=production and PROJECT_BRAIN_AUTH_TOKEN is empty or
// unset, it logs a structured error naming both environment variables and
// returns a non-nil error so the caller (main) can os.Exit(1). In any other
// environment the call is a no-op and returns nil — the permissive dev path
// is preserved so local development never needs a token.
//
// Extracted from main() so the four env×token combos can be unit-tested
// against a bytes.Buffer that captures the slog output.
func enforceProductionAuth(cfg config.Config, logger *slog.Logger) error {
	if cfg.Environment != productionEnvValue {
		return nil
	}
	if cfg.AuthToken != "" {
		return nil
	}

	logger.Error("production startup refused: auth token missing",
		slog.String("reason", authTokenVarName+" unset"),
		slog.String("environment", cfg.Environment),
		slog.String("env_var", envVarName),
		slog.String("token_var", authTokenVarName),
	)
	return fmt.Errorf("refusing to start: %s=%s requires a non-empty %s",
		envVarName, cfg.Environment, authTokenVarName)
}
