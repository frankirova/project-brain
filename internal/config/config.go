package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	defaultEnvironment = "development"
	defaultPort        = "8080"
	defaultShutdownSec = 5
)

// Config contains the minimal process configuration required by the API
// scaffold. Persistence settings are present but optional until the
// PostgreSQL implementation work unit is introduced.
type Config struct {
	Environment  string
	Port         string
	DatabaseDSN  string
	ShutdownSecs int
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
		Environment:  valueOrDefault("PROJECT_BRAIN_ENV", defaultEnvironment),
		Port:         valueOrDefault("PROJECT_BRAIN_API_PORT", defaultPort),
		DatabaseDSN:  os.Getenv("PROJECT_BRAIN_DATABASE_DSN"),
		ShutdownSecs: intEnvOrDefault("PROJECT_BRAIN_SHUTDOWN_SECS", defaultShutdownSec),
	}

	if err := validatePort(cfg.Port); err != nil {
		return Config{}, err
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
