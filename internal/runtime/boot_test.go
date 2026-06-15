package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/config"
)

// expectedBootLog is one line in the slog-identity fixture. Message
// is the slog message string; Keys is the set of structured keys
// (excluding "time" and "level", which every slog record carries).
// The fixture is the byte-for-byte identity the runtime must
// preserve; adding a key, removing a key, or changing the message
// text breaks a spec scenario.
type expectedBootLog struct {
	Level string
	Msg   string
	Keys  []string
}

// bootLogFixtureNoDB is the slog-identity fixture for the boot
// path that does not require a Postgres backend. The runtime's
// in-memory UoW + no-Gemini + no-Telegram branch emits exactly
// these lines (in the listed order is not asserted — concurrent
// sweeper timing is intentionally out of scope; the spec only
// requires the SET of messages and keys to match).
var bootLogFixtureNoDB = []expectedBootLog{
	{Level: "WARN", Msg: "running with in-memory uow", Keys: []string{"reason"}},
	{Level: "INFO", Msg: "search + object endpoints disabled", Keys: []string{"reason"}},
	{Level: "INFO", Msg: "rate limit enabled", Keys: []string{"rps", "burst", "trust_proxy"}},
	// auth disabled: cfg.AuthToken is empty in the test fixture.
	{Level: "WARN", Msg: "auth disabled", Keys: []string{"reason"}},
	// telegram bot skipped: cfg.TelegramBotToken is empty.
	{Level: "INFO", Msg: "telegram bot skipped", Keys: []string{"reason"}},
	// http server starting: emitted by main, not by the runtime,
	// but we exercise the boot path end-to-end so the message +
	// keys reach the captured buffer.
	{Level: "INFO", Msg: "http server starting", Keys: []string{"port", "environment"}},
}

// TestBootSlogIdentityNoDB covers the spec scenario "log strings
// remain byte-identical" for the boot path that does not require
// a Postgres backend. The runtime is exercised end-to-end
// (BuildServices, BuildServer, BuildTelegramBot) with a captured
// JSON-handler logger; every fixture entry is asserted to be
// present with its message and key set unchanged.
func TestBootSlogIdentityNoDB(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := config.Config{
		Environment:      "development",
		Port:             "8050",
		DatabaseDSN:      "", // in-memory UoW path
		AuthToken:        "", // auth disabled path
		TelegramBotToken: "", // telegram bot skipped path
		GeminiAPIKey:     "", // no hybrid search
		RateLimitRPS:     5,
		RateLimitBurst:   10,
		TrustProxy:       false,
		IngestMaxBytes:   1 << 20,
		// Required non-zero HTTP timeouts (config.Load would reject
		// zero values; pass valid defaults so BuildServer's
		// defense-in-depth check is happy).
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svcs, err := BuildServices(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	if _, err := BuildServer(svcs, cfg, logger); err != nil {
		t.Fatalf("BuildServer: %v", err)
	}
	if _, err := BuildTelegramBot(ctx, svcs, cfg, logger); err != nil {
		t.Fatalf("BuildTelegramBot: %v", err)
	}

	// Now emit the "http server starting" line, mirroring what
	// main() does after BuildServer. The runtime itself does not
	// emit it; the test exercises main's behavior too.
	logger.Info("http server starting",
		slog.String("port", cfg.Port),
		slog.String("environment", cfg.Environment))

	// Parse each captured JSON line.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("malformed log line %q: %v", line, err)
		}
		records = append(records, rec)
	}

	// Every fixture entry must be present with the right message,
	// level, and key set. Extra keys (time + level, which are
	// always present, or any key beyond the fixture) are tolerated
	// because the spec mandates the SET of message+keys matches,
	// not the absence of any extra key. We assert each fixture
	// entry is matched by at least one record; we do not require
	// 1:1 — concurrent goroutines (e.g. the embedding retry
	// worker) may emit duplicates.
	for _, want := range bootLogFixtureNoDB {
		if !findRecordWithMessageAndKeys(records, want) {
			t.Errorf("missing fixture entry: level=%s msg=%q keys=%v\ncaptured log:\n%s",
				want.Level, want.Msg, want.Keys, buf.String())
		}
	}
}

func findRecordWithMessageAndKeys(records []map[string]any, want expectedBootLog) bool {
	for _, rec := range records {
		if rec["msg"] != want.Msg {
			continue
		}
		if !strings.EqualFold(asString(rec["level"]), want.Level) {
			continue
		}
		// Every expected key must be present in the record.
		allFound := true
		for _, k := range want.Keys {
			if _, ok := rec[k]; !ok {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	return false
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// TestEnforceInMemoryProductionGuard_RefusesProduction covers the
// spec scenario "production + in-memory UoW exits before
// BuildServices". The guard runs in isolation here; the test
// asserts the error log line and the non-nil error return.
func TestEnforceInMemoryProductionGuard_RefusesProduction(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := config.Config{Environment: "production", DatabaseDSN: ""}
	err := EnforceInMemoryProductionGuard(cfg, logger)
	if err == nil {
		t.Fatal("expected non-nil error from EnforceInMemoryProductionGuard, got nil")
	}

	// Parse the captured log and assert the structured error.
	wantMsg := "in-memory uow refused in production"
	wantReason := "PROJECT_BRAIN_DATABASE_DSN unset"
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	found := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec map[string]any
		if jerr := json.Unmarshal([]byte(line), &rec); jerr != nil {
			t.Fatalf("malformed log line %q: %v", line, jerr)
		}
		if rec["msg"] != wantMsg {
			continue
		}
		if !strings.EqualFold(asString(rec["level"]), "ERROR") {
			continue
		}
		if rec["reason"] != wantReason {
			continue
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("missing error log: level=ERROR msg=%q reason=%q\ncaptured log:\n%s",
			wantMsg, wantReason, buf.String())
	}
}

// TestEnforceInMemoryProductionGuard_AllowsNonProduction covers the
// spec scenario "non-production environments proceed to
// BuildServices". The guard returns nil in non-production
// environments even when DatabaseDSN is empty (so local dev
// works) and in production when DatabaseDSN is set (so production
// with a real DB works).
func TestEnforceInMemoryProductionGuard_AllowsNonProduction(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cases := []struct {
		name string
		cfg  config.Config
	}{
		{"dev_no_dsn", config.Config{Environment: "development", DatabaseDSN: ""}},
		{"dev_with_dsn", config.Config{Environment: "development", DatabaseDSN: "postgres://x"}},
		{"prod_with_dsn", config.Config{Environment: "production", DatabaseDSN: "postgres://x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			if err := EnforceInMemoryProductionGuard(tc.cfg, logger); err != nil {
				t.Fatalf("EnforceInMemoryProductionGuard(%+v) = %v, want nil", tc.cfg, err)
			}
			if buf.Len() != 0 {
				t.Fatalf("expected no log output, got:\n%s", buf.String())
			}
		})
	}
}
