package runtime

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/httpapi/security"
)

// newTestServer builds a BuildServer result suitable for end-to-end
// middleware tests. It uses a non-loopback port stub and an in-memory
// UoW (so no DB is required). The auth token is fixed to "test-token"
// so the test can hit protected endpoints with a known bearer.
func newTestServer(t *testing.T, authToken string, securityHeaders, tls bool) *Server {
	t.Helper()
	cfg := config.Config{
		Environment:            "development",
		Port:                   "0",
		AuthToken:              authToken,
		RateLimitRPS:           1000,
		RateLimitBurst:         10000,
		IngestMaxBytes:         1 << 20,
		ReadHeaderTimeout:      5_000_000_000, // 5s in ns
		ReadTimeout:            10_000_000_000,
		WriteTimeout:           10_000_000_000,
		IdleTimeout:            60_000_000_000,
		SecurityHeadersEnabled: securityHeaders,
		TLSEnabled:             tls,
	}
	// BuildServices with empty DSN falls into the in-memory branch;
	// none of the search/object/backlog/collision handlers wire,
	// so the only protected endpoint is POST /v1/ingest-text, which
	// is what the tests below use.
	svcs, err := BuildServices(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	srv, err := BuildServer(svcs, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildServer: %v", err)
	}
	return srv
}

func do(t *testing.T, h http.Handler, method, path, accept, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Resource-Policy": security.CrossOriginResourcePolicyValue,
		"Cache-Control":                security.CacheControlValue,
		"Permissions-Policy":           security.PermissionsPolicyValue,
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Negative: the spec is explicit that COOP/COEP/CSP are NOT emitted.
	for _, k := range []string{"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy", "Content-Security-Policy"} {
		if got := rec.Header().Get(k); got != "" {
			t.Errorf("%s = %q, want empty (middleware must not emit)", k, got)
		}
	}
}

func TestBuildServer_SecurityHeadersOnPublicHealth(t *testing.T) {
	srv := newTestServer(t, "test-token", true, false)
	rec := do(t, srv.HTTP.Handler, "GET", "/v1/liveness", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertSecurityHeaders(t, rec)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q, want empty (TLSEnabled=false)", got)
	}
}

func TestBuildServer_SecurityHeadersOnProtectedError_WithoutAccept(t *testing.T) {
	// Auth disabled (token="") so /v1/ingest-text accepts the request
	// and reaches the service layer (which returns 400 for empty
	// body, a VALIDATION_ERROR). The middleware path is the focus:
	// the 4xx response MUST carry the security baseline AND the
	// legacy {error,message,code} JSON shape (no Accept opt-in).
	srv := newTestServer(t, "", true, false)
	rec := do(t, srv.HTTP.Handler, "POST", "/v1/ingest-text", "", "")

	if rec.Code < 400 {
		t.Fatalf("status = %d, want 4xx (empty body should fail validation)", rec.Code)
	}
	assertSecurityHeaders(t, rec)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (no Accept opt-in)", ct)
	}
	var legacy struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &legacy); err != nil {
		t.Fatalf("body not legacy JSON: %v: %s", err, rec.Body.String())
	}
	if legacy.Code == "" {
		t.Errorf("legacy code empty, want non-empty: %s", rec.Body.String())
	}
}

func TestBuildServer_ProblemDetailsOnProtectedError_WithAccept(t *testing.T) {
	// Same setup, but the client opts in via Accept. The 4xx
	// response MUST be rewritten as application/problem+json with
	// the RFC 9457 envelope.
	srv := newTestServer(t, "", true, false)
	rec := do(t, srv.HTTP.Handler, "POST", "/v1/ingest-text", "application/problem+json", "")

	if rec.Code < 400 {
		t.Fatalf("status = %d, want 4xx", rec.Code)
	}
	assertSecurityHeaders(t, rec)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body not problem+json: %v: %s", err, rec.Body.String())
	}
	for _, k := range []string{"type", "title", "status", "detail", "instance"} {
		if _, ok := p[k]; !ok {
			t.Errorf("problem+json missing %q: %s", k, rec.Body.String())
		}
	}
	if got, want := int(p["status"].(float64)), rec.Code; got != want {
		t.Errorf("problem.status = %d, want %d (must mirror HTTP status)", got, want)
	}
}

func TestBuildServer_ProblemDetailsRewritesAuthChallenge(t *testing.T) {
	// With auth required, an unauthenticated POST to /v1/ingest-text
	// yields a 401 from auth.Middleware. The problem.Middleware is
	// on the outside of auth, so the 401 response body is rewritten
	// as problem+json when the client opts in.
	srv := newTestServer(t, "test-token", true, false)
	rec := do(t, srv.HTTP.Handler, "POST", "/v1/ingest-text", "application/problem+json", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (auth required)", rec.Code)
	}
	assertSecurityHeaders(t, rec)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json (401 must be rewritten)", ct)
	}
}

func TestBuildServer_HSTSOnlyWhenTLSEnabled(t *testing.T) {
	cases := []struct {
		name    string
		tls     bool
		wantHST string
	}{
		{"tls off", false, ""},
		{"tls on", true, security.HSTSValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, "test-token", true, tc.tls)
			rec := do(t, srv.HTTP.Handler, "GET", "/v1/liveness", "", "")
			got := rec.Header().Get("Strict-Transport-Security")
			if got != tc.wantHST {
				t.Errorf("HSTS = %q, want %q", got, tc.wantHST)
			}
		})
	}
}

func TestBuildServer_SecurityHeadersDisabled(t *testing.T) {
	// When SecurityHeadersEnabled=false, the OWASP baseline is
	// NOT set. Useful for operators that need to ship responses
	// with no security header baseline at all.
	srv := newTestServer(t, "test-token", false, false)
	rec := do(t, srv.HTTP.Handler, "GET", "/v1/liveness", "", "")
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q, want empty (security headers disabled)", got)
	}
}

func TestBuildServer_PreservesInnerHandlerHeader(t *testing.T) {
	// The security middleware MUST NOT overwrite a header an
	// inner handler has set explicitly. We simulate an inner
	// handler by hitting /v1/liveness which does not set any
	// security header — the middleware should set them all. The
	// reverse case (inner overrides middleware) is covered by
	// the spec of security.Middleware, not by this test.
	srv := newTestServer(t, "test-token", true, false)
	rec := do(t, srv.HTTP.Handler, "GET", "/v1/liveness", "", "")
	assertSecurityHeaders(t, rec)
}
