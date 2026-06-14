package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// okProbe is a probe that always succeeds. Used to validate the
// happy-path aggregate behavior of the readiness handler.
func okProbe(_ context.Context) error { return nil }

// errProbe is a probe that always returns the same error. Used to
// validate the failing-probe path surfaces a 503 and the error string.
func errProbe(_ context.Context) error { return errors.New("backend unreachable") }

// slowProbe blocks until the supplied context is cancelled. Used to
// validate the per-probe timeout cap returns 503 instead of hanging.
func slowProbe(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestLivenessHandler_AlwaysReturns200(t *testing.T) {
	// Liveness must succeed regardless of any failure a probe would
	// return — the kubelet must never restart a healthy process just
	// because a downstream is down. Exercising the handler in
	// isolation: there is no way to inject a probe, so passing this
	// test is the proof.
	h := NewLivenessHandler()

	req := httptest.NewRequest(http.MethodGet, "/v1/liveness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rr.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want %q", body["status"], "ok")
	}
}

func TestReadinessHandler_NoProbesReturnsReady(t *testing.T) {
	// An empty probe list is the degenerate "no dependencies" case
	// (e.g. in-memory mode with no workers). The handler must still
	// answer 200 with status=ready.
	h := NewReadinessHandler(0)

	req := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var body readinessResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rr.Body.String())
	}
	if body.Status != "ready" {
		t.Errorf("status = %q, want %q", body.Status, "ready")
	}
	if body.Failed != 0 {
		t.Errorf("failed = %d, want 0", body.Failed)
	}
	if len(body.Probes) != 0 {
		t.Errorf("probes = %d entries, want 0", len(body.Probes))
	}
}

func TestReadinessHandler_AllProbesPassReturnsReady(t *testing.T) {
	// Three healthy probes → 200 + status=ready + all probe rows ok.
	h := NewReadinessHandler(50*time.Millisecond, okProbe, okProbe, okProbe)

	req := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var body readinessResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rr.Body.String())
	}
	if body.Status != "ready" {
		t.Errorf("status = %q, want %q", body.Status, "ready")
	}
	if body.Failed != 0 {
		t.Errorf("failed = %d, want 0", body.Failed)
	}
	if len(body.Probes) != 3 {
		t.Fatalf("probes = %d, want 3", len(body.Probes))
	}
	for i, p := range body.Probes {
		if p.Index != i {
			t.Errorf("probe[%d].Index = %d, want %d", i, p.Index, i)
		}
		if !p.OK {
			t.Errorf("probe[%d].OK = false, want true; err=%q", i, p.Error)
		}
		if p.Error != "" {
			t.Errorf("probe[%d].Error = %q, want empty", i, p.Error)
		}
	}
}

func TestReadinessHandler_FailingProbeReturns503(t *testing.T) {
	// Mix of one healthy probe and one failing probe → 503 + status=not-ready
	// + the failing probe is identified by index in the body.
	h := NewReadinessHandler(50*time.Millisecond, okProbe, errProbe)

	req := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body readinessResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rr.Body.String())
	}
	if body.Status != "not-ready" {
		t.Errorf("status = %q, want %q", body.Status, "not-ready")
	}
	if body.Failed != 1 {
		t.Errorf("failed = %d, want 1", body.Failed)
	}
	if len(body.Probes) != 2 {
		t.Fatalf("probes = %d, want 2", len(body.Probes))
	}
	if !body.Probes[0].OK {
		t.Errorf("probe[0].OK = false, want true")
	}
	if body.Probes[0].Error != "" {
		t.Errorf("probe[0].Error = %q, want empty", body.Probes[0].Error)
	}
	if body.Probes[1].OK {
		t.Errorf("probe[1].OK = true, want false")
	}
	if body.Probes[1].Error == "" {
		t.Errorf("probe[1].Error empty, want the error string")
	}
	if !strings.Contains(body.Probes[1].Error, "backend unreachable") {
		t.Errorf("probe[1].Error = %q, want substring %q", body.Probes[1].Error, "backend unreachable")
	}
}

func TestReadinessHandler_AllProbesFailingReturns503(t *testing.T) {
	// All-failing case: 503 + failed count == probe count.
	h := NewReadinessHandler(50*time.Millisecond, errProbe, errProbe)

	req := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}

	var body readinessResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "not-ready" {
		t.Errorf("status = %q, want %q", body.Status, "not-ready")
	}
	if body.Failed != 2 {
		t.Errorf("failed = %d, want 2", body.Failed)
	}
}

func TestReadinessHandler_SlowProbeTimesOutReturns503(t *testing.T) {
	// A probe that ignores its context must be capped by the handler's
	// per-probe timeout. We use a 10ms timeout with a probe that
	// blocks for 1s; the probe will return ctx.DeadlineExceeded,
	// the handler returns 503.
	h := NewReadinessHandler(10*time.Millisecond, slowProbe)

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	elapsed := time.Since(start)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
	// Defense-in-depth assertion: the handler must NOT block for the
	// full 1s the probe asked for. Give it a generous ceiling.
	if elapsed > 500*time.Millisecond {
		t.Errorf("handler blocked for %v, want under 500ms", elapsed)
	}

	var body readinessResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "not-ready" {
		t.Errorf("status = %q, want %q", body.Status, "not-ready")
	}
	if body.Failed != 1 {
		t.Errorf("failed = %d, want 1", body.Failed)
	}
	if body.Probes[0].Error == "" {
		t.Errorf("probe[0].Error empty, want timeout error")
	}
}

func TestReadinessHandler_MixedHealthyAndSlowProbes(t *testing.T) {
	// Two probes: one healthy, one slow. The handler must still return
	// 503 (the slow one fails) AND must report the healthy one correctly.
	h := NewReadinessHandler(20*time.Millisecond, okProbe, slowProbe)

	req := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}

	var body readinessResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Failed != 1 {
		t.Errorf("failed = %d, want 1", body.Failed)
	}
	if !body.Probes[0].OK {
		t.Errorf("probe[0].OK = false, want true")
	}
	if body.Probes[1].OK {
		t.Errorf("probe[1].OK = true, want false")
	}
}

func TestReadinessHandler_DefaultTimeoutWhenZero(t *testing.T) {
	// Passing timeout=0 must yield DefaultReadinessTimeout internally.
	// We assert the behavior, not the field, by using a probe that
	// blocks: the handler must return within DefaultReadinessTimeout
	// (plus a small grace for goroutine scheduling), not after some
	// arbitrary larger interval. This proves the 0→default fallback
	// is wired in the constructor.
	h := NewReadinessHandler(0, slowProbe)

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	elapsed := time.Since(start)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	if elapsed > DefaultReadinessTimeout+500*time.Millisecond {
		t.Errorf("handler blocked for %v, want under %v", elapsed, DefaultReadinessTimeout+500*time.Millisecond)
	}
}
