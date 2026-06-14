package problem

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// Middleware wraps next so that error responses are rewritten as
// RFC 9457 problem+json when the client opts in via
// `Accept: application/problem+json`.
//
// The middleware is non-invasive: the inner handler does not
// change. It captures the inner handler's response status and
// body via a buffering ResponseWriter, then decides what to emit
// after the inner handler returns:
//
//   - If the response status is success (1xx, 2xx, 3xx), the
//     middleware flushes the captured response unchanged.
//   - If the response status is an error (>= 400) AND the
//     request opts in via Accept, the middleware parses the
//     captured body as a legacy errorResponse and rewrites it
//     as problem+json with the same status.
//   - If the response status is an error but the client did not
//     opt in, the middleware flushes the legacy response
//     unchanged.
//
// The middleware MUST NOT modify the captured body for non-error
// responses; doing so would break JSON streaming and any byte-
// exact contracts the inner handler relies on.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bw := &bufferingWriter{ResponseWriter: w, header: w.Header().Clone()}
		next.ServeHTTP(bw, r)

		optedIn := WantsProblemJSON(r)
		isError := bw.status >= 400

		if !isError || !optedIn {
			// Pass through unchanged.
			bw.flush()
			return
		}

		// Parse the captured legacy body and rewrite as problem+json.
		var legacy legacyErrorBody
		_ = json.Unmarshal(bw.body.Bytes(), &legacy)

		p := FromCode(legacy.Code, legacy.Error, r.URL.Path, bw.status)
		// If the legacy body was empty or didn't match the
		// expected shape, FromCode still returns a Problem with
		// a generic title and the correct status.
		Write(w, p)
	})
}

// bufferingWriter wraps http.ResponseWriter and captures the
// status, header, and body for later inspection. The captured
// data is flushed verbatim when flush() is called.
type bufferingWriter struct {
	http.ResponseWriter
	header     http.Header
	status     int
	body       bytes.Buffer
	wroteHead  bool
	flushedNow bool
}

func (b *bufferingWriter) Header() http.Header {
	return b.header
}

func (b *bufferingWriter) WriteHeader(status int) {
	if b.wroteHead {
		return
	}
	b.wroteHead = true
	b.status = status
}

func (b *bufferingWriter) Write(p []byte) (int, error) {
	if !b.wroteHead {
		// Implicit 200, matching the standard library's
		// http.ResponseWriter contract.
		b.WriteHeader(http.StatusOK)
	}
	return b.body.Write(p)
}

// flush copies the captured status, headers, and body onto the
// underlying ResponseWriter. After flush returns, the caller
// MUST NOT call Write or WriteHeader on this bufferingWriter.
func (b *bufferingWriter) flush() {
	if b.flushedNow {
		return
	}
	b.flushedNow = true

	dst := b.ResponseWriter
	// Copy captured headers (preserving any new ones the inner
	// handler may have set on the wrapper's header).
	for k, vs := range b.header {
		dst.Header()[k] = vs
	}
	if b.wroteHead {
		dst.WriteHeader(b.status)
	}
	if b.body.Len() > 0 {
		_, _ = dst.Write(b.body.Bytes())
	}
}
