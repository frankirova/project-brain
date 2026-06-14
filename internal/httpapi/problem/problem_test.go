package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWantsProblemJSON(t *testing.T) {
	cases := []struct {
		name        string
		accept      string
		setHeader   bool
		want        bool
		description string
	}{
		{
			name:        "exact match",
			accept:      "application/problem+json",
			setHeader:   true,
			want:        true,
			description: "client opts in directly",
		},
		{
			name:        "alongside other types",
			accept:      "application/json, application/problem+json",
			setHeader:   true,
			want:        true,
			description: "client opts in alongside JSON",
		},
		{
			name:        "with q parameter",
			accept:      "application/json, application/problem+json;q=0.9",
			setHeader:   true,
			want:        true,
			description: "client specifies preference via q",
		},
		{
			name:        "case-insensitive",
			accept:      "Application/Problem+JSON",
			setHeader:   true,
			want:        true,
			description: "media types are case-insensitive per RFC 7231",
		},
		{
			name:        "JSON only",
			accept:      "application/json",
			setHeader:   true,
			want:        false,
			description: "client does not opt in",
		},
		{
			name:        "no Accept header",
			setHeader:   false,
			want:        false,
			description: "missing header means no opt-in",
		},
		{
			name:        "wildcard",
			accept:      "*/*",
			setHeader:   true,
			want:        false,
			description: "wildcard does not specifically opt in",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/ingest-text", nil)
			if tc.setHeader {
				r.Header.Set("Accept", tc.accept)
			}
			if got := WantsProblemJSON(r); got != tc.want {
				t.Fatalf("WantsProblemJSON(Accept=%q) = %v, want %v (%s)", tc.accept, got, tc.want, tc.description)
			}
		})
	}
}

func TestWrite(t *testing.T) {
	w := httptest.NewRecorder()
	Write(w, Problem{
		Type:     "https://project-brain.example/problems/invalid-confidence",
		Title:    "Invalid Confidence",
		Status:   http.StatusBadRequest,
		Detail:   "must be between 0 and 1, got 1.5",
		Instance: "/v1/ingest-text",
		Errors: []ErrorItem{
			{Detail: "must be between 0 and 1", Pointer: "/confidence"},
		},
	})

	if got := w.Header().Get("Content-Type"); got != ContentType {
		t.Fatalf("Content-Type = %q, want %q", got, ContentType)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var got Problem
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v (body=%q)", err, w.Body.String())
	}
	if got.Type != "https://project-brain.example/problems/invalid-confidence" {
		t.Errorf("Type = %q, want invalid-confidence URI", got.Type)
	}
	if got.Title != "Invalid Confidence" {
		t.Errorf("Title = %q, want %q", got.Title, "Invalid Confidence")
	}
	if got.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", got.Status, http.StatusBadRequest)
	}
	if got.Detail != "must be between 0 and 1, got 1.5" {
		t.Errorf("Detail = %q, want the offending value in the message", got.Detail)
	}
	if got.Instance != "/v1/ingest-text" {
		t.Errorf("Instance = %q, want request path", got.Instance)
	}
	if len(got.Errors) != 1 || got.Errors[0].Pointer != "/confidence" {
		t.Errorf("Errors = %+v, want one item pointing at /confidence", got.Errors)
	}
}

func TestWriteDefaultsTypeAndStatus(t *testing.T) {
	w := httptest.NewRecorder()
	Write(w, Problem{Title: "Teapot"}) // No Type, no Status

	if got := w.Header().Get("Content-Type"); got != ContentType {
		t.Fatalf("Content-Type = %q, want %q", got, ContentType)
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (the default for missing Status)", w.Code)
	}
	var got Problem
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if got.Type != "about:blank" {
		t.Errorf("Type = %q, want about:blank default", got.Type)
	}
}

func TestTypeFor(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"", "about:blank"},
		{"VALIDATION_ERROR", "https://project-brain.example/problems/validation-error"},
		{"PAYLOAD_TOO_LARGE", "https://project-brain.example/problems/payload-too-large"},
		{"INTERNAL_ERROR", "https://project-brain.example/problems/internal-error"},
		{"UNKNOWN_CODE", "https://project-brain.example/problems/unknown-code"},
	}

	for _, tc := range cases {
		if got := TypeFor(tc.code); got != tc.want {
			t.Errorf("TypeFor(%q) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

func TestTitleFor(t *testing.T) {
	cases := []struct {
		code   string
		status int
		want   string
	}{
		{"VALIDATION_ERROR", http.StatusBadRequest, "Validation Error"},
		{"PAYLOAD_TOO_LARGE", http.StatusRequestEntityTooLarge, "Payload Too Large"},
		{"NOT_FOUND", http.StatusNotFound, "Not Found"},
		{"UNKNOWN_CODE", http.StatusTeapot, "I'm a teapot"}, // http.StatusText fallback
	}

	for _, tc := range cases {
		if got := TitleFor(tc.code, tc.status); got != tc.want {
			t.Errorf("TitleFor(%q, %d) = %q, want %q", tc.code, tc.status, got, tc.want)
		}
	}
}

func TestFromCode(t *testing.T) {
	p := FromCode("VALIDATION_ERROR", "confidence: must be between 0 and 1", "/v1/ingest-text", http.StatusBadRequest)
	if p.Type != "https://project-brain.example/problems/validation-error" {
		t.Errorf("Type = %q", p.Type)
	}
	if p.Title != "Validation Error" {
		t.Errorf("Title = %q", p.Title)
	}
	if p.Status != http.StatusBadRequest {
		t.Errorf("Status = %d", p.Status)
	}
	if p.Detail != "confidence: must be between 0 and 1" {
		t.Errorf("Detail = %q", p.Detail)
	}
	if p.Instance != "/v1/ingest-text" {
		t.Errorf("Instance = %q", p.Instance)
	}
}

// Middleware tests

func TestMiddlewarePassesThrough2xxUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/objects/123", nil)
	r.Header.Set("Accept", "application/problem+json") // opted in

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"123","title":"ok"}`))
	})

	Middleware(inner).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (pass-through for non-error)", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (preserved from inner)", got)
	}
	if got := rec.Body.String(); got != `{"id":"123","title":"ok"}` {
		t.Errorf("body = %q, want the inner response unchanged", got)
	}
}

func TestMiddlewareRewritesErrorWhenOptedIn(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", nil)
	r.Header.Set("Accept", "application/problem+json")

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"confidence: must be between 0 and 1","code":"VALIDATION_ERROR"}`))
	})

	Middleware(inner).ServeHTTP(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (preserved from inner)", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != ContentType {
		t.Errorf("Content-Type = %q, want %q (rewritten as problem+json)", got, ContentType)
	}

	var got Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid problem+json: %v (body=%q)", err, rec.Body.String())
	}
	if got.Title != "Validation Error" {
		t.Errorf("Title = %q, want Validation Error", got.Title)
	}
	if got.Detail != "confidence: must be between 0 and 1" {
		t.Errorf("Detail = %q, want the legacy error message", got.Detail)
	}
	if got.Instance != "/v1/ingest-text" {
		t.Errorf("Instance = %q, want the request path", got.Instance)
	}
	if got.Type != "https://project-brain.example/problems/validation-error" {
		t.Errorf("Type = %q, want the kebab-cased URI for VALIDATION_ERROR", got.Type)
	}
}

func TestMiddlewarePassesThroughErrorWhenNotOptedIn(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", nil)
	r.Header.Set("Accept", "application/json") // legacy client

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad","code":"VALIDATION_ERROR"}`))
	})

	Middleware(inner).ServeHTTP(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (legacy shape preserved)", got)
	}
	if got := rec.Body.String(); got != `{"error":"bad","code":"VALIDATION_ERROR"}` {
		t.Errorf("body = %q, want the legacy errorResponse unchanged", got)
	}
}

func TestMiddlewareRewritesGenericErrorWhenOptedIn(t *testing.T) {
	// Inner handler returns an error with an unparseable body
	// (or no body). The middleware should still emit a valid
	// problem+json response based on the status alone.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/objects/00000000-0000-0000-0000-000000000000", nil)
	r.Header.Set("Accept", "application/problem+json")

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"object not found","code":"NOT_FOUND"}`))
	})

	Middleware(inner).ServeHTTP(rec, r)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Header().Get("Content-Type") != ContentType {
		t.Errorf("Content-Type = %q, want %q", rec.Header().Get("Content-Type"), ContentType)
	}
	var got Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid problem+json: %v", err)
	}
	if got.Title != "Not Found" {
		t.Errorf("Title = %q, want Not Found", got.Title)
	}
	if got.Type != "https://project-brain.example/problems/not-found" {
		t.Errorf("Type = %q, want not-found URI", got.Type)
	}
}
