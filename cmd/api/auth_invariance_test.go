package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/frankirova/project-brain/internal/config"
)

// TestEnforceProductionAuth covers the four env×token combos required by
// change-16 PR 1:
//
//	{production, empty}   -> error  + log names both env vars
//	{production, set}     -> nil    (auth is configured for production)
//	{development, empty}  -> nil    (permissive dev path preserved)
//	{development, set}    -> nil    (token irrelevant outside production)
//
// The slog output is captured into a bytes.Buffer so the failure case can be
// asserted on — the spec requires the log line to name both PROJECT_BRAIN_ENV
// and PROJECT_BRAIN_AUTH_TOKEN.
func TestEnforceProductionAuth(t *testing.T) {
	cases := []struct {
		name        string
		env         string
		token       string
		wantErr     bool
		mustMention []string // substrings the captured slog output must contain
	}{
		{
			name:    "production_empty_token_refuses_startup",
			env:     "production",
			token:   "",
			wantErr: true,
			mustMention: []string{
				"PROJECT_BRAIN_ENV",
				"PROJECT_BRAIN_AUTH_TOKEN",
			},
		},
		{
			name:    "production_with_token_allows_startup",
			env:     "production",
			token:   "super-secret",
			wantErr: false,
		},
		{
			name:    "development_empty_token_allows_startup",
			env:     "development",
			token:   "",
			wantErr: false,
		},
		{
			name:    "development_with_token_allows_startup",
			env:     "development",
			token:   "dev-token",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			// Debug level so nothing is filtered out — invariant logs at Error.
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			cfg := config.Config{Environment: tc.env, AuthToken: tc.token}

			err := enforceProductionAuth(cfg, logger)

			if tc.wantErr && err == nil {
				t.Fatalf("enforceProductionAuth() = nil, want non-nil error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("enforceProductionAuth() = %v, want nil", err)
			}

			for _, sub := range tc.mustMention {
				if !strings.Contains(buf.String(), sub) {
					t.Errorf("captured slog output missing %q\nlog output: %s", sub, buf.String())
				}
			}
		})
	}
}
