package runtime

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/httpapi"
	"github.com/frankirova/project-brain/internal/httpapi/auth"
	"github.com/frankirova/project-brain/internal/httpapi/problem"
	"github.com/frankirova/project-brain/internal/httpapi/ratelimit"
	"github.com/frankirova/project-brain/internal/httpapi/security"
	"github.com/frankirova/project-brain/internal/postgres"
)

// Server is the wired HTTP server bundle BuildServer returns. The
// caller is responsible for ListenAndServe and for passing
// HTTP to RunShutdown. Limiter is exposed because main installs
// the rate-limit log line via the same Limiter (kept here so the
// "rate limit enabled" log is emitted by BuildServer, where the
// pre-refactor main.go emitted it).
type Server struct {
	HTTP    *http.Server
	Limiter *ratelimit.Limiter
}

// BuildServer wires the HTTP router, the per-feature handlers, the
// auth middleware, the rate limiter, and the http.Server literal.
// Behavior is preserved byte-for-byte from the original
// cmd/api/main.go:1-450: the public mux carries only the
// unauthenticated health endpoints, the protected mux carries the
// rest, and the root mux dispatches /v1/health to public and
// everything else through auth -> rate limit -> handler.
//
// The four HTTP transport timeouts (ReadHeaderTimeout, ReadTimeout,
// WriteTimeout, IdleTimeout) are assigned to the http.Server literal
// here. The structural test that asserts they remain locatable on a
// struct literal (TestMainWiresHTTPLiteral, moved alongside) reads
// THIS file by path.
func BuildServer(svcs Services, cfg config.Config, logger *slog.Logger) (*Server, error) {
	handler := httpapi.NewIngestTextHandler(svcs.IngestService, cfg.IngestMaxBytes)

	// Readiness probes: built from the same dependencies the public mux
	// has. When a Postgres backend is wired we add a DB ping (the only
	// hard dependency on the readiness path for Fase 3); in-memory mode
	// has no probes, which the readiness handler treats as "ready"
	// (no dependencies to check). Workers/queues (Fase 4) will be
	// appended here when they land.
	var readinessProbes []httpapi.ReadinessProbe
	if pgDB, ok := svcs.UoW.(*postgres.DB); ok && pgDB != nil {
		pool := pgDB.Pool()
		readinessProbes = append(readinessProbes, func(ctx context.Context) error {
			return pool.Ping(ctx)
		})
	}

	// Public mux: only the health probes. No auth, no rate limit —
	// health must work even when the service is being abused or auth
	// is broken. The kubelet cannot present a bearer token, so the
	// /v1/readiness endpoint is intentionally unauthenticated.
	publicMux := http.NewServeMux()
	publicMux.Handle("GET /v1/health", &httpapi.HealthHandler{})
	publicMux.Handle("GET /v1/liveness", httpapi.NewLivenessHandler())
	publicMux.Handle("GET /v1/readiness", httpapi.NewReadinessHandler(0, readinessProbes...))

	// Protected mux: ingest endpoint goes through auth then rate limit.
	// Search and object endpoints are also protected (they read tenant
	// data). They are only registered when a retriever was built above.
	protectedMux := http.NewServeMux()
	protectedMux.Handle("POST /v1/ingest-text", handler)
	if svcs.SearchHandler != nil {
		protectedMux.Handle("GET /v1/search", svcs.SearchHandler)
		protectedMux.Handle("GET /v1/objects/{id}", svcs.ObjectHandler)
	}
	if svcs.CollisionHandler != nil {
		protectedMux.Handle("POST /v1/check-collision", svcs.CollisionHandler)
	}
	if svcs.BacklogHandler != nil {
		protectedMux.Handle("GET /v1/backlog", svcs.BacklogHandler)
	}
	if svcs.SddDocumentHandler != nil {
		protectedMux.Handle("GET /v1/sdd-document", svcs.SddDocumentHandler)
	}

	limiter := ratelimit.New(cfg.RateLimitRPS, cfg.RateLimitBurst, 10*time.Minute)
	limiter.SetTrustProxy(cfg.TrustProxy)
	logger.Info("rate limit enabled",
		slog.Float64("rps", cfg.RateLimitRPS),
		slog.Float64("burst", cfg.RateLimitBurst),
		slog.Bool("trust_proxy", cfg.TrustProxy))

	if cfg.AuthToken == "" {
		logger.Warn("auth disabled", slog.String("reason", "PROJECT_BRAIN_AUTH_TOKEN unset"))
	} else {
		logger.Info("auth enabled", slog.String("scheme", "bearer"))
	}

	// Compose: top-level mux routes /v1/health to public, everything else
	// to the protected chain.
	//
	// Middleware order (outer to inner):
	//   security.Middleware      every response (2xx/4xx/5xx, health and API)
	//     rootMux                dispatch to public or protected
	//       publicMux            health probes (legacy shape, no problem+json)
	//       problem.Middleware   rewrites error responses as RFC 9457 problem+json
	//                            when the client opts in via Accept. MUST wrap
	//                            auth+ratelimit so 401 challenges and 429
	//                            rejections also get the new shape.
	//         auth.Middleware    401 on missing/invalid bearer
	//           rateLimit.Middleware  429 on per-IP rate limit
	//             protectedMux        the API handlers
	//
	// Security headers wrap the OUTERMOST handler so 401/429/500 — including
	// responses generated by auth and ratelimit — all carry the OWASP
	// baseline. Health responses get security headers too (they are
	// harmless on plain JSON).
	rootMux := http.NewServeMux()
	rootMux.Handle("GET /v1/health", publicMux)
	// Also route /v1/liveness and /v1/readiness to the public mux.
	// The kubelet cannot present a bearer token, so these probes MUST
	// be reachable even when PROJECT_BRAIN_AUTH_TOKEN is set. The
	// earlier wiring only routed /v1/health, which silently 401'd
	// the liveness/readiness probes once auth was enabled. Caught
	// while landing this PR's end-to-end middleware tests.
	rootMux.Handle("GET /v1/liveness", publicMux)
	rootMux.Handle("GET /v1/readiness", publicMux)
	protectedChain := problem.Middleware(
		auth.Middleware(cfg.AuthToken)(
			limiter.Middleware(protectedMux),
		),
	)
	rootMux.Handle("/", protectedChain)

	if cfg.SecurityHeadersEnabled {
		logger.Info("security headers enabled",
			slog.Bool("hsts", cfg.TLSEnabled))
	} else {
		logger.Warn("security headers disabled",
			slog.String("reason", "PROJECT_BRAIN_SECURITY_HEADERS=false"))
	}

	// Order: auth first, then rate limit, then handler. Rate limit runs
	// after auth so unauthenticated floods don't consume buckets.
	//
	// Transport timeouts. Load() already rejects non-positive values,
	// so under normal config the panic below is unreachable. It stays
	// as defense in depth: if a future refactor hands in a zero-value
	// Config (e.g. in a test), the server MUST NOT start with an
	// unprotected transport.
	readHeaderTimeout := cfg.HTTPReadHeaderTimeout()
	readTimeout := cfg.HTTPReadTimeout()
	writeTimeout := cfg.HTTPWriteTimeout()
	idleTimeout := cfg.HTTPIdleTimeout()
	if readHeaderTimeout <= 0 || readTimeout <= 0 || writeTimeout <= 0 || idleTimeout <= 0 {
		logger.Error("http transport timeouts must all be > 0",
			slog.Duration("read_header_timeout", readHeaderTimeout),
			slog.Duration("read_timeout", readTimeout),
			slog.Duration("write_timeout", writeTimeout),
			slog.Duration("idle_timeout", idleTimeout))
		panic("http transport timeouts must all be > 0")
	}
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           http.Handler(rootMux),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Security headers middleware wraps the entire server handler (outermost).
	// When disabled, the inner rootMux dispatches unchanged — useful for
	// tests and for operators that need to ship responses with no security
	// header baseline at all.
	if cfg.SecurityHeadersEnabled {
		server.Handler = security.Middleware(cfg.TLSEnabled)(server.Handler)
	}

	return &Server{HTTP: server, Limiter: limiter}, nil
}
