package config

import (
	"testing"
	"time"
)

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

func TestLoadHTTPDurationDefaults(t *testing.T) {
	// Clear all four *_SECS env vars so defaults win.
	t.Setenv("PROJECT_BRAIN_HTTP_READ_HEADER_TIMEOUT_SECS", "")
	t.Setenv("PROJECT_BRAIN_HTTP_READ_TIMEOUT_SECS", "")
	t.Setenv("PROJECT_BRAIN_HTTP_WRITE_TIMEOUT_SECS", "")
	t.Setenv("PROJECT_BRAIN_HTTP_IDLE_TIMEOUT_SECS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.ReadHeaderTimeout != defaultHTTPReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %s, want %s", cfg.ReadHeaderTimeout, defaultHTTPReadHeaderTimeout)
	}
	if cfg.ReadTimeout != defaultHTTPReadTimeout {
		t.Errorf("ReadTimeout = %s, want %s", cfg.ReadTimeout, defaultHTTPReadTimeout)
	}
	if cfg.WriteTimeout != defaultHTTPWriteTimeout {
		t.Errorf("WriteTimeout = %s, want %s", cfg.WriteTimeout, defaultHTTPWriteTimeout)
	}
	if cfg.IdleTimeout != defaultHTTPIdleTimeout {
		t.Errorf("IdleTimeout = %s, want %s", cfg.IdleTimeout, defaultHTTPIdleTimeout)
	}

	// Accessors must return the same values as the underlying fields.
	if got := cfg.HTTPReadHeaderTimeout(); got != cfg.ReadHeaderTimeout {
		t.Errorf("HTTPReadHeaderTimeout() = %s, want %s", got, cfg.ReadHeaderTimeout)
	}
	if got := cfg.HTTPReadTimeout(); got != cfg.ReadTimeout {
		t.Errorf("HTTPReadTimeout() = %s, want %s", got, cfg.ReadTimeout)
	}
	if got := cfg.HTTPWriteTimeout(); got != cfg.WriteTimeout {
		t.Errorf("HTTPWriteTimeout() = %s, want %s", got, cfg.WriteTimeout)
	}
	if got := cfg.HTTPIdleTimeout(); got != cfg.IdleTimeout {
		t.Errorf("HTTPIdleTimeout() = %s, want %s", got, cfg.IdleTimeout)
	}
}

func TestLoadHTTPTimeoutOverride(t *testing.T) {
	// Set each env var independently and assert it lands on the
	// matching field. The other three stay at the default — proves
	// the loader does not bleed values across fields.
	cases := []struct {
		name       string
		envKey     string
		envValue   string
		checkField string
		want       time.Duration
	}{
		{
			name:       "read header timeout override",
			envKey:     "PROJECT_BRAIN_HTTP_READ_HEADER_TIMEOUT_SECS",
			envValue:   "10",
			checkField: "ReadHeaderTimeout",
			want:       10 * time.Second,
		},
		{
			name:       "read timeout override",
			envKey:     "PROJECT_BRAIN_HTTP_READ_TIMEOUT_SECS",
			envValue:   "20",
			checkField: "ReadTimeout",
			want:       20 * time.Second,
		},
		{
			name:       "write timeout override",
			envKey:     "PROJECT_BRAIN_HTTP_WRITE_TIMEOUT_SECS",
			envValue:   "45",
			checkField: "WriteTimeout",
			want:       45 * time.Second,
		},
		{
			name:       "idle timeout override",
			envKey:     "PROJECT_BRAIN_HTTP_IDLE_TIMEOUT_SECS",
			envValue:   "180",
			checkField: "IdleTimeout",
			want:       180 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Clear the other three so we can assert "only the
			// targeted field changed".
			t.Setenv("PROJECT_BRAIN_HTTP_READ_HEADER_TIMEOUT_SECS", "")
			t.Setenv("PROJECT_BRAIN_HTTP_READ_TIMEOUT_SECS", "")
			t.Setenv("PROJECT_BRAIN_HTTP_WRITE_TIMEOUT_SECS", "")
			t.Setenv("PROJECT_BRAIN_HTTP_IDLE_TIMEOUT_SECS", "")
			t.Setenv(tc.envKey, tc.envValue)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}

			switch tc.checkField {
			case "ReadHeaderTimeout":
				if cfg.ReadHeaderTimeout != tc.want {
					t.Errorf("ReadHeaderTimeout = %s, want %s", cfg.ReadHeaderTimeout, tc.want)
				}
				if cfg.ReadTimeout != defaultHTTPReadTimeout {
					t.Errorf("ReadTimeout = %s, want default %s", cfg.ReadTimeout, defaultHTTPReadTimeout)
				}
			case "ReadTimeout":
				if cfg.ReadTimeout != tc.want {
					t.Errorf("ReadTimeout = %s, want %s", cfg.ReadTimeout, tc.want)
				}
				if cfg.ReadHeaderTimeout != defaultHTTPReadHeaderTimeout {
					t.Errorf("ReadHeaderTimeout = %s, want default %s", cfg.ReadHeaderTimeout, defaultHTTPReadHeaderTimeout)
				}
			case "WriteTimeout":
				if cfg.WriteTimeout != tc.want {
					t.Errorf("WriteTimeout = %s, want %s", cfg.WriteTimeout, tc.want)
				}
				if cfg.IdleTimeout != defaultHTTPIdleTimeout {
					t.Errorf("IdleTimeout = %s, want default %s", cfg.IdleTimeout, defaultHTTPIdleTimeout)
				}
			case "IdleTimeout":
				if cfg.IdleTimeout != tc.want {
					t.Errorf("IdleTimeout = %s, want %s", cfg.IdleTimeout, tc.want)
				}
				if cfg.WriteTimeout != defaultHTTPWriteTimeout {
					t.Errorf("WriteTimeout = %s, want default %s", cfg.WriteTimeout, defaultHTTPWriteTimeout)
				}
			}
		})
	}
}

func TestLoadHTTPTimeoutMalformedFallsBack(t *testing.T) {
	// Malformed env value (non-integer) should fall back to the
	// default with a warning — the loader does NOT fail closed on
	// parse errors, it just warns and uses the safe default. The
	// > 0 validation in Load is the authoritative gate.
	t.Setenv("PROJECT_BRAIN_HTTP_READ_TIMEOUT_SECS", "not-a-number")
	t.Setenv("PROJECT_BRAIN_HTTP_READ_HEADER_TIMEOUT_SECS", "")
	t.Setenv("PROJECT_BRAIN_HTTP_WRITE_TIMEOUT_SECS", "")
	t.Setenv("PROJECT_BRAIN_HTTP_IDLE_TIMEOUT_SECS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.ReadTimeout != defaultHTTPReadTimeout {
		t.Errorf("ReadTimeout = %s, want default %s (malformed env should fall back)", cfg.ReadTimeout, defaultHTTPReadTimeout)
	}
}

func TestLoadSecurityHeadersDefaults(t *testing.T) {
	// Both env vars unset. Security headers default ON; TLS
	// defaults OFF. A fresh dev / test instance running on
	// plain HTTP should not advertise an HSTS upgrade.
	t.Setenv("PROJECT_BRAIN_SECURITY_HEADERS", "")
	t.Setenv("PROJECT_BRAIN_TLS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !cfg.SecurityHeadersEnabled {
		t.Errorf("SecurityHeadersEnabled = false, want true (default)")
	}
	if cfg.TLSEnabled {
		t.Errorf("TLSEnabled = true, want false (default)")
	}
}

func TestLoadSecurityHeadersOverrides(t *testing.T) {
	cases := []struct {
		name        string
		headersEnv  string
		tlsEnv      string
		wantHeaders bool
		wantTLS     bool
	}{
		{name: "both off", headersEnv: "false", tlsEnv: "false", wantHeaders: false, wantTLS: false},
		{name: "headers off, tls on", headersEnv: "false", tlsEnv: "true", wantHeaders: false, wantTLS: true},
		{name: "headers on, tls on", headersEnv: "true", tlsEnv: "true", wantHeaders: true, wantTLS: true},
		{name: "1 / 0 accepted", headersEnv: "1", tlsEnv: "0", wantHeaders: true, wantTLS: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROJECT_BRAIN_SECURITY_HEADERS", tc.headersEnv)
			t.Setenv("PROJECT_BRAIN_TLS", tc.tlsEnv)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() returned error: %v", err)
			}
			if cfg.SecurityHeadersEnabled != tc.wantHeaders {
				t.Errorf("SecurityHeadersEnabled = %v, want %v", cfg.SecurityHeadersEnabled, tc.wantHeaders)
			}
			if cfg.TLSEnabled != tc.wantTLS {
				t.Errorf("TLSEnabled = %v, want %v", cfg.TLSEnabled, tc.wantTLS)
			}
		})
	}
}
