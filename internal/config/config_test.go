package config

import "testing"

func TestLoadUsesDefaultsWithoutExternalServices(t *testing.T) {
	t.Setenv("PROJECT_BRAIN_ENV", "")
	t.Setenv("PROJECT_BRAIN_API_PORT", "")
	t.Setenv("PROJECT_BRAIN_DATABASE_DSN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Environment != defaultEnvironment {
		t.Fatalf("Environment = %q, want %q", cfg.Environment, defaultEnvironment)
	}
	if cfg.Port != defaultPort {
		t.Fatalf("Port = %q, want %q", cfg.Port, defaultPort)
	}
	if cfg.DatabaseDSN != "" {
		t.Fatalf("DatabaseDSN = %q, want empty", cfg.DatabaseDSN)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("PROJECT_BRAIN_ENV", "test")
	t.Setenv("PROJECT_BRAIN_API_PORT", "9090")
	t.Setenv("PROJECT_BRAIN_DATABASE_DSN", "postgres://user:pass@example.test:5432/project_brain")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Environment != "test" {
		t.Fatalf("Environment = %q, want test", cfg.Environment)
	}
	if cfg.Port != "9090" {
		t.Fatalf("Port = %q, want 9090", cfg.Port)
	}
	if cfg.DatabaseDSN == "" {
		t.Fatal("DatabaseDSN is empty, want configured value")
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("PROJECT_BRAIN_API_PORT", "not-a-port")

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid port error")
	}
}
