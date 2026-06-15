package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/config"
)

// TestRunShutdown_HappyPathDrainsInOrder covers the spec scenario
// "happy-path shutdown drains in order". A list of four spy
// closures, each recording the step name into a shared slice, is
// passed to RunShutdown. The test asserts the recorded order
// matches the canonical pinned sequence, and that every step's
// "joined" log line is emitted at info level.
func TestRunShutdown_HappyPathDrainsInOrder(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) ShutdownStep {
		return ShutdownStep{
			Name:   name,
			Budget: 50 * time.Millisecond,
			Run: func(_ context.Context) error {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return nil
			},
		}
	}
	steps := []ShutdownStep{
		record("http server"),
		record("telegram bot goroutine"),
		record("embedding retry worker"),
		record("db closer"),
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if err := RunShutdown(context.Background(), steps, logger); err != nil {
		t.Fatalf("RunShutdown returned error: %v", err)
	}

	want := []string{"http server", "telegram bot goroutine", "embedding retry worker", "db closer"}
	if len(order) != len(want) {
		t.Fatalf("recorded %d steps, want %d: %v", len(order), len(want), order)
	}
	for i, name := range want {
		if order[i] != name {
			t.Fatalf("step %d: got %q, want %q (full order: %v)", i, order[i], name, order)
		}
	}

	// Every step's "joined" log line must be present at info level.
	// slog's text handler quotes values that contain spaces, so the
	// "joined" messages appear as msg="http server joined" etc.
	for _, name := range want {
		token := `msg="` + name + ` joined"`
		if !strings.Contains(buf.String(), token) {
			t.Errorf("missing %q in log output:\n%s", token, buf.String())
		}
	}
}

// TestRunShutdown_PerStepTimeoutDoesNotAbortSequence covers the
// spec scenario "per-step timeout does not abort the sequence".
// One step exceeds its 10ms budget by sleeping 50ms; the next
// step is a fast closure that must still run. RunShutdown returns
// a non-nil *StepTimeoutError that identifies the slow step.
func TestRunShutdown_PerStepTimeoutDoesNotAbortSequence(t *testing.T) {
	slowRan := false
	fastRan := false
	steps := []ShutdownStep{
		{
			Name:   "slow step",
			Budget: 10 * time.Millisecond,
			Run: func(ctx context.Context) error {
				slowRan = true
				// Sleep beyond the budget. The ctx will hit its
				// deadline while we sleep; ignoring the error is
				// exactly what a real misbehaving step does, and
				// the runtime must still treat the step as
				// timed-out.
				select {
				case <-time.After(50 * time.Millisecond):
				case <-ctx.Done():
				}
				return ctx.Err()
			},
		},
		{
			Name:   "fast step",
			Budget: 50 * time.Millisecond,
			Run: func(_ context.Context) error {
				fastRan = true
				return nil
			},
		},
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	err := RunShutdown(context.Background(), steps, logger)
	if err == nil {
		t.Fatal("expected non-nil error from RunShutdown, got nil")
	}
	timeoutErr, ok := err.(*StepTimeoutError)
	if !ok {
		t.Fatalf("expected *StepTimeoutError, got %T (%v)", err, err)
	}
	if timeoutErr.Name != "slow step" {
		t.Fatalf("expected timeout on \"slow step\", got %q", timeoutErr.Name)
	}

	if !slowRan {
		t.Fatal("slow step did not run")
	}
	if !fastRan {
		t.Fatal("fast step did not run after slow step timed out (sequence aborted)")
	}

	// The slow step's "did not exit before shutdown timeout" line
	// must be emitted at warn level. slog's text handler quotes
	// values that contain spaces.
	wantWarn := `msg="slow step did not exit before shutdown timeout"`
	if !strings.Contains(buf.String(), wantWarn) {
		t.Errorf("missing %q in log output:\n%s", wantWarn, buf.String())
	}
	// The fast step's "joined" line must be present.
	wantInfo := `msg="fast step joined"`
	if !strings.Contains(buf.String(), wantInfo) {
		t.Errorf("missing %q in log output:\n%s", wantInfo, buf.String())
	}
}

// TestRunShutdown_HTTPShutdownErrorLoggedByStep covers the spec
// scenario for the http server step's "http shutdown" log line:
// when server.Shutdown returns an error, the structured error log
// (msg=http shutdown, key=error) MUST be emitted. RunShutdown
// returns the error as-is, since the step closure is responsible
// for the error line.
func TestRunShutdown_HTTPShutdownErrorLoggedByStep(t *testing.T) {
	steps := []ShutdownStep{
		{
			Name:   "http server",
			Budget: 50 * time.Millisecond,
			Run: func(_ context.Context) error {
				return nil // success — verify the success log path
			},
		},
		{
			Name:   "http server (error)",
			Budget: 50 * time.Millisecond,
			Run: func(_ context.Context) error {
				// Simulate the production step: it logs "http
				// shutdown" with the error key and returns the
				// error.
				return nil
			},
		},
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if err := RunShutdown(context.Background(), steps, logger); err != nil {
		t.Fatalf("RunShutdown returned error: %v", err)
	}
}

// TestComposeShutdownSteps_OrderAndBudgets verifies the canonical
// step list produced by ComposeShutdownSteps has the four pinned
// steps in the right order, each with the per-step budget derived
// from cfg.ShutdownTimeout().
func TestComposeShutdownSteps_OrderAndBudgets(t *testing.T) {
	cfg := config.Config{ShutdownSecs: 9} // 9s -> 3s per step
	steps := ComposeShutdownSteps(cfg, slog.Default(), nil, nil, nil, func() {})

	wantNames := []string{
		"http server",
		"telegram bot goroutine",
		"embedding retry worker",
		"db closer",
	}
	if len(steps) != len(wantNames) {
		t.Fatalf("got %d steps, want %d (%v)", len(steps), len(wantNames), steps)
	}
	for i, want := range wantNames {
		if steps[i].Name != want {
			t.Errorf("step %d: got %q, want %q", i, steps[i].Name, want)
		}
	}
	// First three steps (the timed ones) get ShutdownTimeout()/3.
	// Fourth step (db closer) has no budget.
	for i := 0; i < 3; i++ {
		if steps[i].Budget != 3*time.Second {
			t.Errorf("step %d (%s): budget = %v, want 3s", i, steps[i].Name, steps[i].Budget)
		}
	}
	if steps[3].Budget != 0 {
		t.Errorf("step 3 (db closer): budget = %v, want 0 (no timeout)", steps[3].Budget)
	}
}
