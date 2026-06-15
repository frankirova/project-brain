package runtime

import (
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/telegram"
)

// TestTypedNilDetectorGuard_NilPointerBecomesInterfaceNil covers
// the spec scenario "typed-nil detector becomes interface nil".
//
// The Telegram handler's nil check (tgCfg.Detector == nil) only
// works if the interface itself is nil. Assigning a typed-nil
// *app.CollisionDetector to a telegram.Config.Detector (a
// collisionChecker interface) produces a NON-NIL interface
// wrapping a nil pointer — a well-known Go gotcha. Without the
// guard, the first inbound message would panic when the handler
// called h.detector.Detect(...).
//
// The runtime's applyTypedNilDetectorGuard explicitly sets
// cfg.Detector to a true nil interface when the source pointer is
// nil, so the handler's nil check takes the disabled-detector
// path.
func TestTypedNilDetectorGuard_NilPointerBecomesInterfaceNil(t *testing.T) {
	// Sanity: the typed-nil trap. Wrapping a typed-nil
	// *app.CollisionDetector in an `any` interface produces a
	// non-nil interface wrapping a nil pointer. This is the
	// exact pattern the bot builder must neutralize.
	var d *app.CollisionDetector
	var raw any = d
	if raw == nil {
		t.Fatal("setup sanity: typed-nil wrapped in any should be non-nil")
	}

	// Apply the runtime's guard. After this, the Config's
	// Detector field must be a true nil interface.
	cfg := &telegram.Config{Service: nil}
	applyTypedNilDetectorGuard(cfg, d)

	if cfg.Detector != nil {
		t.Fatalf("expected Detector to be interface nil, got %#v (typed-nil trap leaked)", cfg.Detector)
	}
}

// TestTypedNilDetectorGuard_NonNilPassesThrough covers the spec
// scenario "non-nil detector is wired through unchanged". A real
// (non-nil) collision detector must reach the handler unmodified:
// the guard is a one-way conversion of nil to nil, never a
// side-channel that drops a valid dependency.
func TestTypedNilDetectorGuard_NonNilPassesThrough(t *testing.T) {
	d := &app.CollisionDetector{}
	cfg := &telegram.Config{Service: nil}
	applyTypedNilDetectorGuard(cfg, d)

	if cfg.Detector == nil {
		t.Fatal("expected Detector to be non-nil, got nil")
	}
}
