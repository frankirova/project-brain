// Package security implements the OWASP Secure Headers Project
// 2025 baseline (canonical source: ci/headers_add.json, updated
// 2026-05-19) for the project-brain JSON API. The middleware
// sets a small set of well-known response headers on every
// response, including the Cache-Control directive that OWASP
// recommends for every response by default.
//
// The middleware is hand-rolled (~50 lines) rather than wrapping
// a library (e.g. unrolled/secure). The surface is small,
// stable, and public knowledge; libraries in this space have
// footguns (e.g. still emitting X-XSS-Protection on request,
// no Cache-Control default, no Server stripping) that are
// easier to avoid with explicit code.
package security

import (
	"net/http"
)

// PermissionsPolicyValue is the locked-down Permissions-Policy
// header value. Every feature the API does not use is denied
// with an empty allowlist. Update this list if a future
// endpoint legitimately needs a feature; the principle is
// "deny by default, allow explicitly".
const PermissionsPolicyValue = "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()"

// CrossOriginResourcePolicyValue restricts cross-origin reads
// of responses to same-site requests. This is a defense-in-depth
// header that complements the SameSite cookie / CSRF posture
// already enforced by the auth middleware.
const CrossOriginResourcePolicyValue = "same-site"

// CacheControlValue is the OWASP ci/headers_add.json default
// for every response: prevent any caching of API responses.
const CacheControlValue = "no-store, max-age=0"

// HSTSValue is the Mozilla recommended baseline HSTS header.
// The 'preload' directive is intentionally NOT included —
// hstspreload.org 2024 guidance explicitly advises against
// preloading from new deployments.
const HSTSValue = "max-age=63072000; includeSubDomains"

// Middleware returns a middleware that sets the OWASP 2025
// baseline headers + Cache-Control on every response, plus
// HSTS when enableHSTS is true.
//
// The middleware sets headers BEFORE calling the next handler.
// This guarantees the headers are present on every response,
// including:
//   - 2xx success responses
//   - 401 auth challenges
//   - 429 rate-limit rejections
//   - 500 panic-recovery responses
//
// The middleware does NOT overwrite a header that an inner
// handler or middleware has already set. This lets future
// endpoints opt out of the baseline (e.g. an endpoint that
// legitimately needs a different Referrer-Policy).
//
// The middleware does NOT emit COOP, COEP, or CSP. COOP/COEP
// are inert on JSON responses (per Mozilla and OWASP 2025).
// CSP belongs on the future web client that consumes the API,
// not on the API itself.
func Middleware(enableHSTS bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			setIfAbsent(h, "X-Content-Type-Options", "nosniff")
			setIfAbsent(h, "X-Frame-Options", "DENY")
			setIfAbsent(h, "Referrer-Policy", "no-referrer")
			setIfAbsent(h, "Permissions-Policy", PermissionsPolicyValue)
			setIfAbsent(h, "Cross-Origin-Resource-Policy", CrossOriginResourcePolicyValue)
			setIfAbsent(h, "Cache-Control", CacheControlValue)
			if enableHSTS {
				setIfAbsent(h, "Strict-Transport-Security", HSTSValue)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// setIfAbsent sets key to value only if the header is not
// already present on h. The check is case-insensitive
// (matching net/http's behavior for the http.CanonicalMIMEHeaderKey
// transformation).
func setIfAbsent(h http.Header, key, value string) {
	if h.Get(key) == "" {
		h.Set(key, value)
	}
}
