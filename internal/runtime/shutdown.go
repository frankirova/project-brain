package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/frankirova/project-brain/internal/config"
)

// StepTimeoutError is returned by RunShutdown when a single step
// exceeded its per-step deadline. It identifies the timed-out step
// by Name so the orchestrator / operator can correlate the error
// with the structured "did not exit before shutdown timeout"
// warning RunShutdown already logged.
type StepTimeoutError struct {
	// Name is the ShutdownStep.Name of the step that timed out.
	Name string
}

func (e *StepTimeoutError) Error() string {
	return fmt.Sprintf("shutdown step timed out: %s", e.Name)
}

// ShutdownStep is a single shutdown phase. Budget is the per-step
// deadline (zero = no deadline). Run executes the step under a
// derived context that honors Budget (via context.WithTimeout).
//
// On a nil return RunShutdown logs "{Name} joined" at info. On a
// context.DeadlineExceeded return RunShutdown logs "{Name} did not
// exit before shutdown timeout" at warn and wraps the error in a
// StepTimeoutError so the orchestrator can identify which step
// stalled. Other non-nil errors are returned as-is — the step's
// Run closure is responsible for its own error logging (e.g.,
// server.Shutdown's "http shutdown" error line).
type ShutdownStep struct {
	Name   string
	Budget time.Duration
	Run    func(ctx context.Context) error
}

// RunShutdown runs the steps in order. Each step is given its own
// context.WithTimeout(ctx, step.Budget) when Budget > 0. A single
// step's failure or timeout does NOT abort the sequence — every
// step is attempted so a shutdown always tries to drain all
// resources. The returned error is the first non-nil error from any
// step (nil when every step succeeded).
//
// This is the spec-pinned shutdown sequence the change-19 contract
// requires: order is server → botWG → retryDone → dbCloser in the
// production path, and every step is bounded by a per-step deadline
// derived from the configured ShutdownTimeout.
func RunShutdown(ctx context.Context, steps []ShutdownStep, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	var firstErr error
	for _, step := range steps {
		if err := runOneStep(ctx, step, logger); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runOneStep(ctx context.Context, step ShutdownStep, logger *slog.Logger) error {
	if step.Budget <= 0 {
		// No timeout. The step is responsible for its own logging
		// (e.g., the db closer has no log line at all). Run with
		// the parent context.
		return step.Run(ctx)
	}
	stepCtx, cancel := context.WithTimeout(ctx, step.Budget)
	defer cancel()
	err := step.Run(stepCtx)
	if err == nil {
		logger.Info(step.Name + " joined")
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		logger.Warn(step.Name + " did not exit before shutdown timeout")
		return &StepTimeoutError{Name: step.Name}
	}
	// Non-timeout error: return as-is. The step's Run closure is
	// expected to have already emitted the structured error line
	// (e.g. "http shutdown" with slog.String("error", ...)).
	return err
}

// ComposeShutdownSteps returns the canonical step list for the
// project-brain API. Order is:
//
//  1. http server drain        (server.Shutdown under its budget)
//  2. telegram bot goroutine   (wait on the botWG under its budget)
//  3. embedding retry worker   (wait on the done channel under its budget)
//  4. db closer                (single function call, no budget)
//
// Each timed step is allocated one third of the configured
// ShutdownTimeout. The DB closer is unbounded — it is expected to
// be a single Close() call. nil pointers (no http.Server, no botWG,
// no retryDone) produce a step that returns nil immediately so the
// sequence is robust to optional dependencies.
//
// logger is captured in the per-step closures so the "http
// shutdown" error line emitted by step 1 lands on the same logger
// the rest of the boot uses. This is what the spec-pinned slog
// identity contract requires: the runtime must not introduce a
// parallel log sink for the shutdown path.
func ComposeShutdownSteps(cfg config.Config, logger *slog.Logger, httpServer any, botWG syncLike, retryDone <-chan struct{}, dbCloser func()) []ShutdownStep {
	budget := cfg.ShutdownTimeout() / 3
	if budget <= 0 {
		// Defensive default: a zero/negative configured budget would
		// disable every per-step timeout, defeating the spec. Pick
		// a small positive default (1s) so the steps still get a
		// deadline. The pre-refactor main.go used cfg.ShutdownTimeout()
		// (default 5s) for every step, so this is no worse.
		budget = time.Second
	}
	steps := []ShutdownStep{}

	// Step 1: HTTP server drain. nil server is a no-op step so
	// callers that haven't built a server yet (or test spies that
	// skip this step) can still call RunShutdown. The Shutdown
	// error is logged with the same logger the rest of the boot
	// used; slog identity is preserved.
	steps = append(steps, ShutdownStep{
		Name:   "http server",
		Budget: budget,
		Run: func(ctx context.Context) error {
			if httpServer == nil {
				return nil
			}
			s, ok := httpServer.(interface{ Shutdown(context.Context) error })
			if !ok {
				return nil
			}
			if err := s.Shutdown(ctx); err != nil {
				logger.Error("http shutdown", slog.String("error", err.Error()))
				return err
			}
			return nil
		},
	})

	// Step 2: Telegram bot goroutine. We don't import sync here as
	// a hard dependency type so the helper accepts a small
	// interface; *sync.WaitGroup satisfies it.
	steps = append(steps, ShutdownStep{
		Name:   "telegram bot goroutine",
		Budget: budget,
		Run: func(ctx context.Context) error {
			if botWG == nil {
				return nil
			}
			done := make(chan struct{})
			go func() { botWG.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})

	// Step 3: Embedding retry worker. nil channel = worker never
	// started; this step is a no-op.
	steps = append(steps, ShutdownStep{
		Name:   "embedding retry worker",
		Budget: budget,
		Run: func(ctx context.Context) error {
			if retryDone == nil {
				return nil
			}
			select {
			case <-retryDone:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})

	// Step 4: DB closer. No budget — it is a single function call.
	steps = append(steps, ShutdownStep{
		Name: "db closer",
		Run: func(_ context.Context) error {
			dbCloser()
			return nil
		},
	})

	return steps
}

// syncLike is a tiny interface that *sync.WaitGroup satisfies. It
// is package-local so the helper signature does not force a
// sync.WaitGroup type on callers (the typed-nil test in particular
// wants to pass a real WaitGroup without leaking the type into the
// public API).
type syncLike interface {
	Wait()
}
