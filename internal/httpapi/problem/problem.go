// Package problem implements RFC 9457 (formerly RFC 7807) Problem
// Details for HTTP APIs. The wire format is a pure superset of
// RFC 7807 — there are no breaking changes between the two specs.
//
// Rollout uses standard HTTP content negotiation (RFC 9457 §4):
// clients opt in by sending `Accept: application/problem+json`.
// Clients that do not advertise the media type continue to
// receive the legacy `{"error": "...", "code": "..."}` JSON shape.
// This is the migration pattern explicitly endorsed by the
// specification.
//
// Type URIs are stable HTTPS identifiers. They do not need to
// resolve to human-readable pages today, but using HTTPS URIs
// from day one avoids a breaking change if they later become
// dereferenceable.
package problem

import (
	"encoding/json"
	"net/http"
	"strings"
)

// ContentType is the media type for problem+json responses per
// RFC 9457 §3. The IANA registration was updated to reference
// RFC 9457 when it obsoleted RFC 7807.
const ContentType = "application/problem+json"

// Problem is the RFC 9457 §3.1 problem-details envelope. The
// Errors field is the spec's own §3 example extension for
// multi-field validation errors: each item carries a Detail
// (human-readable) and a Pointer (JSON Pointer, RFC 6901, to the
// offending field).
type Problem struct {
	// Type is a stable HTTPS URI that identifies the problem
	// class. Defaults to "about:blank" when empty.
	Type string `json:"type"`

	// Title is a short, human-readable summary of the problem
	// class. SHOULD NOT change between occurrences of the same
	// problem type.
	Title string `json:"title"`

	// Status mirrors the HTTP status code. MUST match the status
	// line of the HTTP response.
	Status int `json:"status"`

	// Detail is a human-readable explanation specific to this
	// occurrence. May include the offending value or context.
	Detail string `json:"detail,omitempty"`

	// Instance is a URI reference that identifies the specific
	// occurrence. We use the request path; it is stable for the
	// duration of a request and not PII.
	Instance string `json:"instance,omitempty"`

	// Errors is a non-standard extension following the spec's
	// own §3 example for multi-field validation. Each item
	// carries a detail and a JSON Pointer (RFC 6901) to the
	// offending field in the request body.
	Errors []ErrorItem `json:"errors,omitempty"`
}

// ErrorItem is one entry in the multi-field validation extension.
// Detail is the human-readable message; Pointer is a JSON Pointer
// (RFC 6901) addressing the offending field.
type ErrorItem struct {
	Detail  string `json:"detail,omitempty"`
	Pointer string `json:"pointer,omitempty"`
}

// legacyErrorBody is the legacy `errorResponse` shape the
// middleware parses from the captured inner-handler body. It
// carries the human-readable message (Error) and the legacy
// structured code (Code) that the middleware translates into a
// problem Type URI.
type legacyErrorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// ProblemTypeBase is the prefix for every problem Type URI. URIs
// follow the format `https://project-brain.example/problems/<code>`
// where <code> is the kebab-case form of the legacy code.
const problemTypeBase = "https://project-brain.example/problems/"

// TypeFor returns the canonical problem Type URI for a legacy
// error code. Unknown codes fall through to "about:blank" so the
// middleware always has a stable URI to emit.
func TypeFor(code string) string {
	if code == "" {
		return "about:blank"
	}
	return problemTypeBase + toKebab(code)
}

// toKebab converts SCREAMING_SNAKE_CASE to kebab-case.
func toKebab(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "_", "-"))
}

// WantsProblemJSON reports whether the request's Accept header
// advertises the application/problem+json media type. The check
// is case-insensitive and ignores parameters (e.g. ";q=0.9").
func WantsProblemJSON(r *http.Request) bool {
	if r == nil {
		return false
	}
	for _, accept := range r.Header.Values("Accept") {
		for _, part := range strings.Split(accept, ",") {
			mediaType := strings.TrimSpace(part)
			if i := strings.Index(mediaType, ";"); i >= 0 {
				mediaType = strings.TrimSpace(mediaType[:i])
			}
			if strings.EqualFold(mediaType, ContentType) {
				return true
			}
		}
	}
	return false
}

// TitleFor returns a human-readable title for a legacy error code.
// Falls back to the canonical HTTP status text when the code is
// not in the table.
func TitleFor(code string, status int) string {
	if t, ok := legacyTitles[code]; ok {
		return t
	}
	return http.StatusText(status)
}

// legacyTitles maps the legacy `errorResponse.code` values to
// problem+json titles. The mapping mirrors the legacy human-
// readable messages in the existing handlers.
var legacyTitles = map[string]string{
	"VALIDATION_ERROR":  "Validation Error",
	"PAYLOAD_TOO_LARGE": "Payload Too Large",
	"NOT_FOUND":         "Not Found",
	"INTERNAL_ERROR":    "Internal Server Error",
	"INVALID_CURSOR":    "Invalid Cursor",
	"UNAUTHORIZED":      "Unauthorized",
}

// Write serializes p as a problem+json response. It sets the
// Content-Type header and writes the HTTP status. The body is
// encoded with json.NewEncoder so the output is deterministic.
func Write(w http.ResponseWriter, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Status == 0 {
		// Fall back to 500 if the caller didn't set one. Most
		// callers set both, so this is the safety net.
		p.Status = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(p.Status)

	// Encode the body. We don't care about the error here —
	// the response status is already set, and a write failure
	// is the client's problem.
	_ = json.NewEncoder(w).Encode(p)
}

// FromCode builds a Problem from a legacy error code, a detail
// message, and an instance URI (typically the request path). The
// title is derived from the code, the status from the legacy
// `code -> status` table, and the Type from TypeFor(code).
//
// Unknown codes fall through to a generic Problem with the given
// status and a stable Type URI derived from the code.
func FromCode(code, detail, instance string, status int) Problem {
	return Problem{
		Type:     TypeFor(code),
		Title:    TitleFor(code, status),
		Status:   status,
		Detail:   detail,
		Instance: instance,
	}
}
