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
)

// Config contains the minimal process configuration required by the API
// scaffold. Persistence settings are present but optional until the
// PostgreSQL implementation work unit is introduced.
type Config struct {
	Environment       string
	Port              string
	DatabaseDSN       string
	TelegramBotToken  string
	AuthToken         string
	ShutdownSecs      int
	RateLimitRPS      float64
	RateLimitBurst    float64
	TrustProxy        bool
	IngestMaxBytes    int64
}

// ShutdownTimeout returns the configured shutdown grace period.
func (c Config) ShutdownTimeout() time.Duration {
	secs := c.ShutdownSecs
	if secs <= 0 {
		secs = defaultShutdownSec
	}
	return time.Duration(secs) * time.Second
}

// Load reads configuration from environment variables and applies safe local
// defaults so the scaffold can run without external services.
func Load() (Config, error) {
	cfg := Config{
		Environment:      valueOrDefault("PROJECT_BRAIN_ENV", defaultEnvironment),
		Port:             valueOrDefault("PROJECT_BRAIN_API_PORT", defaultPort),
		DatabaseDSN:      os.Getenv("PROJECT_BRAIN_DATABASE_DSN"),
		TelegramBotToken: os.Getenv("PROJECT_BRAIN_TELEGRAM_BOT_TOKEN"),
		AuthToken:        os.Getenv("PROJECT_BRAIN_AUTH_TOKEN"),
		ShutdownSecs:     intEnvOrDefault("PROJECT_BRAIN_SHUTDOWN_SECS", defaultShutdownSec),
		RateLimitRPS:     floatEnvOrDefault("PROJECT_BRAIN_RATE_LIMIT_RPS", 5),
		RateLimitBurst:   floatEnvOrDefault("PROJECT_BRAIN_RATE_LIMIT_BURST", 10),
		TrustProxy:       boolEnvOrDefault("PROJECT_BRAIN_TRUST_PROXY", false),
		IngestMaxBytes:   int64EnvOrDefault("PROJECT_BRAIN_INGEST_MAX_BYTES", 1<<20),
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
