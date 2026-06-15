package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/config"
)

// TestBootSlogIdentityHybridSearch_DSNGated covers the spec
// scenario "hybrid-search and sweeper logs are present" for the
// hybrid-search log line. Requires a live Postgres test database
// (PROJECT_BRAIN_TEST_DATABASE_DSN); skips otherwise so the
// short-mode test suite stays runnable without external services.
//
// The test asserts the runtime emits the "hybrid search +
// collision detection enabled" line with the pre-refactor message
// and key set (provider, model, dimensions). The Gemini API key
// is a fake: NewEmbedder does not make HTTP calls at construction
// time, so the boot path runs end-to-end without contacting the
// real Gemini service.
func TestBootSlogIdentityHybridSearch_DSNGated(t *testing.T) {
	dsn := os.Getenv("PROJECT_BRAIN_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("PROJECT_BRAIN_TEST_DATABASE_DSN not set; skipping DSN-gated hybrid-search log identity test")
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := config.Config{
		Environment:       "development",
		Port:              "8050",
		DatabaseDSN:       dsn,
		AuthToken:         "",
		TelegramBotToken:  "",
		GeminiAPIKey:      "fake-key-for-test", // NewEmbedder does not call out at construction
		RateLimitRPS:      5,
		RateLimitBurst:    10,
		TrustProxy:        false,
		IngestMaxBytes:    1 << 20,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	svcs, err := BuildServices(ctx, cfg, logger)
	if err != nil {
		// pgxpool.Ping may fail in a CI environment that has the
		// env var set but no actual DB reachable. Skip on ping
		// failure rather than fail the short-mode suite.
		t.Skipf("BuildServices with live DSN failed (likely no live DB in this CI): %v", err)
	}
	_ = svcs // services are wired; we only care about the log output

	want := expectedBootLog{
		Level: "INFO",
		Msg:   "hybrid search + collision detection enabled",
		Keys:  []string{"provider", "model", "dimensions"},
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if !findRecordWithMessageAndKeys(parseJSONLines(t, lines), want) {
		t.Fatalf("missing fixture entry: %+v\ncaptured log:\n%s", want, buf.String())
	}
}

func parseJSONLines(t *testing.T, lines []string) []map[string]any {
	t.Helper()
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
	return records
}
