package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// DefaultReadinessTimeout caps how long a single probe may run before the
// readiness handler returns 503. Two seconds matches the typical kubelet
// probe period (3s) without exceeding it on a slow DB ping.
const DefaultReadinessTimeout = 2 * time.Second

// LivenessHandler answers GET /v1/liveness. It performs no dependency
// checks and never returns 5xx while the process is running — that is the
// whole point of splitting liveness from readiness (Kubernetes restarts a
// failing pod on liveness failure, even when the failure is a downstream
// the pod cannot control).
type LivenessHandler struct{}

// NewLivenessHandler returns a LivenessHandler. The constructor exists so
// future configuration (build hash, start time) can be added without
// touching every call site.
func NewLivenessHandler() *LivenessHandler { return &LivenessHandler{} }

// ServeHTTP writes a 200 with a small JSON body identifying the process
// as alive. Mirrors the existing HealthHandler body so any tool already
// parsing /v1/health can parse /v1/liveness identically.
func (h *LivenessHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Default().Error("response encode failed",
			slog.String("handler", "liveness"),
			slog.String("error", err.Error()))
	}
}

// ReadinessProbe is a single dependency check. A nil return means the
// dependency is healthy. The probe MUST respect the context's deadline
// (a hung probe that ignores ctx will block the handler past its
// per-probe timeout cap).
type ReadinessProbe func(context.Context) error

// probeResult is the per-probe entry in the readiness response body.
// Index lets operators correlate failures with the probe list defined
// in the composition root when probes are registered in code.
type probeResult struct {
	Index int    `json:"index"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// readinessResponse is the JSON wire type for /v1/readiness.
type readinessResponse struct {
	Status string        `json:"status"` // "ready" or "not-ready"
	Failed int           `json:"failed"`
	Probes []probeResult `json:"probes"`
}

// ReadinessHandler answers GET /v1/readiness. It runs every registered
// probe under a per-probe timeout and returns 200 only when ALL probes
// return nil. Any failure (including a probe that exceeds its timeout)
// yields 503 with a JSON body listing the per-probe results.
type ReadinessHandler struct {
	timeout time.Duration
	probes  []ReadinessProbe
}

// NewReadinessHandler builds a ReadinessHandler. timeout is the per-probe
// cap; pass 0 to use DefaultReadinessTimeout. probes are the dependency
// checks to run on every request. An empty probe list yields 200 with
// status=ready (degenerate case: no dependencies to check).
func NewReadinessHandler(timeout time.Duration, probes ...ReadinessProbe) *ReadinessHandler {
	if timeout <= 0 {
		timeout = DefaultReadinessTimeout
	}
	return &ReadinessHandler{timeout: timeout, probes: probes}
}

// ServeHTTP runs every probe under a derived context with timeout and
// writes 200/503 + the per-probe result list. The handler does not hold
// state between requests, so it is safe for concurrent use.
func (h *ReadinessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	results := make([]probeResult, len(h.probes))
	allOK := true
	for i, probe := range h.probes {
		ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
		err := probe(ctx)
		cancel()
		results[i] = probeResult{Index: i, OK: err == nil}
		if err != nil {
			allOK = false
			results[i].Error = err.Error()
		}
	}

	status := http.StatusOK
	statusStr := "ready"
	failed := 0
	if !allOK {
		status = http.StatusServiceUnavailable
		statusStr = "not-ready"
		for _, r := range results {
			if !r.OK {
				failed++
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(readinessResponse{
		Status: statusStr,
		Failed: failed,
		Probes: results,
	}); err != nil {
		slog.Default().Error("response encode failed",
			slog.String("handler", "readiness"),
			slog.String("error", err.Error()))
	}
}
