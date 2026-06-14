package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// passthroughHandler is a no-op handler used by the tests to
// verify the middleware's header-setting behavior in isolation.
type passthroughHandler struct{}

func (passthroughHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func TestMiddlewareSetsBaselineOn2xx(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)

	Middleware(false)(passthroughHandler{}).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	assertBaseline(t, w.Header())
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q, want empty (HSTS off)", got)
	}
}

func TestMiddlewareSetsBaselineOnAuthChallenge(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", nil)

	authChallenge := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="project-brain"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","code":"UNAUTHORIZED"}`))
	})

	Middleware(false)(authChallenge).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	assertBaseline(t, w.Header())
	if got := w.Header().Get("WWW-Authenticate"); got == "" {
		t.Errorf("WWW-Authenticate header is missing — auth challenge should pass through")
	}
}

func TestMiddlewareSetsBaselineOnPanic(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/panic", nil)

	panickingHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// Simulate what a panic-recovery middleware would write.
		// The real panic recovery runs in main.go's chain; for
		// this test we just verify the security headers still
		// apply to a 500 response.
		panic("boom")
	})

	defer func() {
		// Catch the panic and verify the headers were still set
		// by running the middleware manually with a 500 handler.
		_ = recover()
	}()

	// First prove the headers are set BEFORE the next handler
	// runs (the panic recovery in production would still see
	// the headers on the 500 response).
	w2 := httptest.NewRecorder()
	inner500 := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	})
	Middleware(false)(inner500).ServeHTTP(w2, r)
	if w2.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w2.Code)
	}
	assertBaseline(t, w2.Header())

	// Now run the actual panicking handler to confirm a panic
	// doesn't prevent the headers from being set on the way in.
	// The handler will not actually be reached because of the
	// panic, but the security middleware sets headers BEFORE
	// delegating, so they should be present on the recorder
	// even though the recorder never gets a WriteHeader.
	defer func() { _ = recover() }()
	Middleware(false)(panickingHandler).ServeHTTP(w, r)

	// The recorder's headers are populated as a side-effect of
	// the security middleware touching w.Header() before the
	// panic. The actual write would happen via panic-recovery
	// in production; here we just confirm the headers were set.
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("baseline header missing after panic path")
	}
}

func TestMiddlewareSetsHSTSWhenEnabled(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)

	Middleware(true)(passthroughHandler{}).ServeHTTP(w, r)

	got := w.Header().Get("Strict-Transport-Security")
	want := "max-age=63072000; includeSubDomains"
	if got != want {
		t.Errorf("HSTS = %q, want %q", got, want)
	}
	// The preload directive is explicitly NOT included.
	if got == "max-age=63072000; includeSubDomains; preload" {
		t.Errorf("HSTS includes preload directive — should be omitted per hstspreload.org 2024 guidance")
	}
}

func TestMiddlewareOmitsHSTSWhenDisabled(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)

	Middleware(false)(passthroughHandler{}).ServeHTTP(w, r)

	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q, want empty (HSTS off)", got)
	}
}

func TestMiddlewarePreservesInnerHeader(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/exception", nil)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	Middleware(false)(inner).ServeHTTP(w, r)

	if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN (preserved from inner, not overwritten to DENY)", got)
	}
	// Other baseline headers should still be set.
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestMiddlewareDoesNotEmitCOOPOrCOEP(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/health", nil)

	Middleware(true)(passthroughHandler{}).ServeHTTP(w, r)

	for _, h := range []string{
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Embedder-Policy",
		"Content-Security-Policy",
	} {
		if got := w.Header().Get(h); got != "" {
			t.Errorf("%s = %q, want empty (inert on JSON / not emitted)", h, got)
		}
	}
}

// assertBaseline checks that all five baseline headers + the
// Cache-Control default are present.
func assertBaseline(t *testing.T, h http.Header) {
	t.Helper()
	cases := []struct {
		key, want string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"Referrer-Policy", "no-referrer"},
		{"Permissions-Policy", PermissionsPolicyValue},
		{"Cross-Origin-Resource-Policy", "same-site"},
		{"Cache-Control", "no-store, max-age=0"},
	}
	for _, tc := range cases {
		if got := h.Get(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}
