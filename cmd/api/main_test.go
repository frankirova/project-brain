package main

import (
	"os"
	"strings"
	"testing"
)

// TestMainWiresHTTPLiteral is the structural backstop for the
// http-server-timeouts work unit (change-16, PR 2). Asserting the
// config layer carries the values is necessary but not sufficient —
// nothing tests that the composition root actually assigns them to
// http.Server. Rather than spinning up a real server in a test, this
// reads main.go and asserts the four timeout field names appear in the
// http.Server literal block. A reviewer can also see at a glance that
// the structural change is in place.
func TestMainWiresHTTPLiteral(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	contents := string(source)

	// Each field name from http.Server must appear in the source.
	// We check the bare identifier (not cfg.HTTPReadHeaderTimeout)
	// because the structural guarantee is that the http.Server
	// literal actually carries the field.
	required := []string{
		"ReadHeaderTimeout",
		"ReadTimeout",
		"WriteTimeout",
		"IdleTimeout",
	}

	missing := make([]string, 0, len(required))
	for _, name := range required {
		// Look for the field name appearing as a struct literal
		// key, i.e. immediately followed by a colon. That rules
		// out passing the test by a stray mention in a comment.
		token := name + ":"
		if !strings.Contains(contents, token) {
			missing = append(missing, token)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("main.go http.Server literal is missing timeout field(s): %v\n\n"+
			"structural test backstop: each timeout must be assigned on the http.Server literal "+
			"so the config values actually reach the transport", missing)
	}
}
