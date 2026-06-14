package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	defaultEnvironment = "development"
	defaultPort        = "8050"
	defaultShutdownSec = 5

	// HTTP server transport timeouts. ReadHeaderTimeout is the
	// Slowloris defense (must be set; everything else can be
	// relaxed per-deployment). The other three are tuned to
	// typical REST traffic: short reads, modest writes, generous
	// idle reuse.
	defaultHTTPReadHeaderTimeout = 5 * time.Second
	defaultHTTPReadTimeout       = 10 * time.Second
	defaultHTTPWriteTimeout      = 10 * time.Second
	defaultHTTPIdleTimeout       = 60 * time.Second
)

// Config contains the minimal process configuration required by the API
// scaffold. Persistence settings are present but optional until the
// PostgreSQL implementation work unit is introduced.
type Config struct {
	Environment      string
	Port             string
	DatabaseDSN      string
	TelegramBotToken string
	AuthToken        string
	GeminiAPIKey     string
	ShutdownSecs     int
	RateLimitRPS     float64
	RateLimitBurst   float64
	TrustProxy       bool
	IngestMaxBytes   int64

	// HTTP server transport timeouts. All four MUST be > 0; the
	// loader returns an error otherwise. These mirror the field
	// names on http.Server so the composition root can pass them
	// through without renaming.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// SecurityHeadersEnabled gates the OWASP 2025 baseline
	// headers middleware (nosniff, DENY, no-referrer, locked-down
	// Permissions-Policy, same-site CORP, Cache-Control:
	// no-store, max-age=0). Default true. Set to false to
	// disable the entire middleware for an environment where
	// the operator needs to ship responses with no security
	// header baseline at all.
	SecurityHeadersEnabled bool

	// TLSEnabled gates the Strict-Transport-Security header.
	// Set to true when the API is being served over HTTPS
	// (typically behind a TLS-terminating reverse proxy in
	// production). Default false so a dev / test instance
	// running on plain HTTP does not tell clients to upgrade
	// to HTTPS for two years.
	TLSEnabled bool
}

// ShutdownTimeout returns the configured shutdown grace period.
func (c Config) ShutdownTimeout() time.Duration {
	secs := c.ShutdownSecs
	if secs <= 0 {
		secs = defaultShutdownSec
	}
	return time.Duration(secs) * time.Second
}

// HTTPReadHeaderTimeout returns the configured read-header timeout.
func (c Config) HTTPReadHeaderTimeout() time.Duration {
	return c.ReadHeaderTimeout
}

// HTTPReadTimeout returns the configured read timeout.
func (c Config) HTTPReadTimeout() time.Duration {
	return c.ReadTimeout
}

// HTTPWriteTimeout returns the configured write timeout.
func (c Config) HTTPWriteTimeout() time.Duration {
	return c.WriteTimeout
}

// HTTPIdleTimeout returns the configured idle timeout.
func (c Config) HTTPIdleTimeout() time.Duration {
	return c.IdleTimeout
}

// Load reads configuration from environment variables and applies safe local
// defaults so the scaffold can run without external services.
func Load() (Config, error) {
	cfg := Config{
		Environment:            valueOrDefault("PROJECT_BRAIN_ENV", defaultEnvironment),
		Port:                   valueOrDefault("PROJECT_BRAIN_API_PORT", defaultPort),
		DatabaseDSN:            os.Getenv("PROJECT_BRAIN_DATABASE_DSN"),
		TelegramBotToken:       os.Getenv("PROJECT_BRAIN_TELEGRAM_BOT_TOKEN"),
		AuthToken:              os.Getenv("PROJECT_BRAIN_AUTH_TOKEN"),
		GeminiAPIKey:           os.Getenv("PROJECT_BRAIN_GEMINI_API_KEY"),
		ShutdownSecs:           intEnvOrDefault("PROJECT_BRAIN_SHUTDOWN_SECS", defaultShutdownSec),
		RateLimitRPS:           floatEnvOrDefault("PROJECT_BRAIN_RATE_LIMIT_RPS", 5),
		RateLimitBurst:         floatEnvOrDefault("PROJECT_BRAIN_RATE_LIMIT_BURST", 10),
		TrustProxy:             boolEnvOrDefault("PROJECT_BRAIN_TRUST_PROXY", false),
		IngestMaxBytes:         int64EnvOrDefault("PROJECT_BRAIN_INGEST_MAX_BYTES", 1<<20),
		ReadHeaderTimeout:      durationEnvOrDefault("PROJECT_BRAIN_HTTP_READ_HEADER_TIMEOUT_SECS", defaultHTTPReadHeaderTimeout),
		ReadTimeout:            durationEnvOrDefault("PROJECT_BRAIN_HTTP_READ_TIMEOUT_SECS", defaultHTTPReadTimeout),
		WriteTimeout:           durationEnvOrDefault("PROJECT_BRAIN_HTTP_WRITE_TIMEOUT_SECS", defaultHTTPWriteTimeout),
		IdleTimeout:            durationEnvOrDefault("PROJECT_BRAIN_HTTP_IDLE_TIMEOUT_SECS", defaultHTTPIdleTimeout),
		SecurityHeadersEnabled: boolEnvOrDefault("PROJECT_BRAIN_SECURITY_HEADERS", true),
		TLSEnabled:             boolEnvOrDefault("PROJECT_BRAIN_TLS", false),
	}

	if err := validatePort(cfg.Port); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitRPS > 1000 {
		return Config{}, fmt.Errorf("PROJECT_BRAIN_RATE_LIMIT_RPS=%g exceeds sanity cap of 1000", cfg.RateLimitRPS)
	}
	if cfg.RateLimitBurst > 10000 {
		return Config{}, fmt.Errorf("PROJECT_BRAIN_RATE_LIMIT_BURST=%g exceeds sanity cap of 10000", cfg.RateLimitBurst)
	}

	// HTTP transport timeouts MUST be positive. A zero or negative
	// value disables the corresponding defense (or makes the server
	// reject every request immediately) — the spec requires an
	// explicit error so the operator notices a misconfiguration
	// instead of silently running an unprotected server.
	if cfg.ReadHeaderTimeout <= 0 {
		return Config{}, fmt.Errorf("PROJECT_BRAIN_HTTP_READ_HEADER_TIMEOUT_SECS must be > 0")
	}
	if cfg.ReadTimeout <= 0 {
		return Config{}, fmt.Errorf("PROJECT_BRAIN_HTTP_READ_TIMEOUT_SECS must be > 0")
	}
	if cfg.WriteTimeout <= 0 {
		return Config{}, fmt.Errorf("PROJECT_BRAIN_HTTP_WRITE_TIMEOUT_SECS must be > 0")
	}
	if cfg.IdleTimeout <= 0 {
		return Config{}, fmt.Errorf("PROJECT_BRAIN_HTTP_IDLE_TIMEOUT_SECS must be > 0")
	}

	return cfg, nil
}

func valueOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}

func validatePort(port string) error {
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return fmt.Errorf("PROJECT_BRAIN_API_PORT must be a valid TCP port")
	}

	return nil
}

func intEnvOrDefault(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
		fmt.Fprintf(os.Stderr, "warning: %s=%q is not a valid positive integer, using default %d\n", key, v, fallback)
	}
	return fallback
}

func int64EnvOrDefault(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func floatEnvOrDefault(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func boolEnvOrDefault(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "1" || v == "true" || v == "TRUE" || v == "yes"
	}
	return fallback
}

// durationEnvOrDefault reads a *_SECS integer env var and returns it as a
// time.Duration. Malformed or non-positive values fall back to the supplied
// default with a warning — the per-field > 0 check in Load is the
// authoritative validation (it returns an error rather than silently
// substituting).
func durationEnvOrDefault(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
		fmt.Fprintf(os.Stderr, "warning: %s=%q is not a valid positive integer, using default %s\n", key, v, fallback)
	}
	return fallback
}
