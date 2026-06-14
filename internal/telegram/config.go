package telegram

import (
	"errors"
	"log/slog"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/app/inmem"
	"github.com/go-telegram/bot"
	"github.com/google/uuid"
)

// Config carries the dependencies the handler needs to be wired at
// startup. The handler exposes exactly one public constructor —
// New(Config) — per the change-18 telegram-bot-adapter spec; the 7
// legacy NewHandler* overloads were collapsed to this struct + a
// private newHandlerWithStore seam.
//
// Two fields are required at construction time: Service (the
// ingest pipeline the bot feeds into) and a Sender (the Telegram
// adapter that actually pushes messages). The Sender field is the
// test seam — the production composition root leaves it nil and
// New builds a real *telegramSender from Bot, while tests inject a
// fakeSender (or any other Sender implementation) directly.
//
// All other fields are optional and propagate as "not configured"
// fallbacks at the call site (e.g. nil Backlog disables /backlog,
// nil Validator answers "no disponible" for the rv: validate
// button). The same nil-tolerant defaults the old constructors
// used live in applyDefaults so New(Config{...}) with a partial
// Config is a valid program.
type Config struct {
	// Service is the ingest pipeline the bot feeds incoming
	// messages into. Required.
	Service *app.IngestTextService

	// Detector runs the "what existing knowledge does this clash
	// with?" check on incoming text. Nil falls back to the
	// legacy direct-ingest path (no validation prompt).
	Detector collisionChecker

	// RawInputs persists a raw_input row for every incoming
	// message before collision detection runs (REQ-05). Nil
	// disables the staging table — local dev and unit tests.
	RawInputs app.RawInputRepository

	// Bot is the Telegram bot handle used to build the default
	// Sender when Sender is nil. The bot is wired lazily: New
	// stores it as-is and telegramSender.SendMessage captures
	// it the first time DefaultHandler fires.
	Bot *bot.Bot

	// Sender is the test seam for outbound Telegram traffic.
	// When non-nil, the handler uses it directly. When nil, New
	// builds a real *telegramSender from Bot. The fakeSender
	// the unit tests use is the canonical "Sender != nil" path.
	Sender Sender

	// Pending persists the in-flight collision-validation
	// rows. Nil installs the in-memory fallback (local dev,
	// unit tests, in-memory UoW).
	Pending pendingStore

	// Backlog is the "what's next on the human review queue?"
	// query. Nil disables the /backlog command.
	Backlog backlogLister

	// Finder hydrates the KnowledgeObject behind a backlog
	// card so the card can show a content preview. Nil
	// produces Title/Summary-only cards.
	Finder app.KnowledgeObjectFinder

	// ReviewStore persists the opaque tokens the inline
	// keyboard buttons carry. Nil installs the in-memory
	// fallback (same shape as Pending).
	ReviewStore reviewActionStore

	// Validator is the slice of *app.ValidateObjectService
	// the rv: validate/deprecate callback dispatches to. Nil
	// answers "no disponible" for those buttons; the backlog
	// render path is unaffected.
	Validator reviewValidator

	// Debater is the slice of *app.ObjectDebateService the
	// rv: debate/resolve callback dispatches to. Nil answers
	// "no disponible" for those buttons.
	Debater reviewDebator

	// Logger receives structured logs from the handler. Nil
	// falls back to slog.Default() — the same behaviour the
	// old constructors had.
	Logger *slog.Logger

	// NewToken mints a fresh opaque token per button. Nil
	// falls back to uuid.NewString(). Tests override the
	// equivalent field on the Handler struct directly to
	// pin a deterministic token sequence.
	NewToken func() string
}

// applyDefaults fills the zero-value fields with their
// nil-tolerant fallbacks. Called by New before Validate so the
// Validate step can reason about the populated Config.
func (c *Config) applyDefaults() {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.NewToken == nil {
		c.NewToken = uuid.NewString
	}
	if c.Pending == nil {
		c.Pending = inmem.NewPendingValidationStore()
	}
	if c.ReviewStore == nil {
		c.ReviewStore = inmem.NewTelegramReviewActionStore()
	}
	if c.Sender == nil && c.Bot != nil {
		c.Sender = &telegramSender{b: c.Bot}
	}
}

// Validate enforces the cross-field invariants New requires
// before it can build a Handler. Service is mandatory; the
// Sender surface (Sender or Bot) must be wired. Optional
// dependencies (Detector, Backlog, Validator, Debater) are
// checked for nil by the handler at call time, not here.
func (c *Config) Validate() error {
	if c.Service == nil {
		return errors.New("telegram: Config.Service is required")
	}
	if c.Sender == nil && c.Bot == nil {
		return errors.New("telegram: Config.Sender (or Config.Bot) is required")
	}
	return nil
}

// New constructs a Handler from a Config. It applies the
// nil-tolerant defaults, validates the cross-field invariants,
// and delegates to the private newHandlerWithStore seam. This
// is the single public entry point the change-18 spec requires;
// the 7 legacy NewHandler* overloads (4 public, 3 private) were
// collapsed here per #1736.
func New(cfg Config) (*Handler, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return newHandlerWithStore(cfg), nil
}

// newHandlerWithStore is the private composition seam: it
// accepts a Config and returns the wired Handler. Both the
// public New and the package's own _test.go files call it
// directly — same package, no exported wrapper is needed.
//
// The seam applies the same nil-tolerant defaults New does so
// test helpers can pass a partial Config (e.g., one without
// ReviewStore or Pending) and still get the in-memory fallback
// the production path would install. It does NOT call
// Validate: callers that want the cross-field invariants
// enforced must go through New; the seam stays tolerant so
// tests can wire one slice of the dependency graph in
// isolation.
//
// The name preserves the original 6-arg seam's intent (the
// Sender, store, and remaining dependencies are all "wired"
// here) so a future reader can grep for newHandlerWithStore
// across the change history and find the original test seam.
func newHandlerWithStore(cfg Config) *Handler {
	cfg.applyDefaults()
	return &Handler{
		service:     cfg.Service,
		detector:    cfg.Detector,
		rawInputs:   cfg.RawInputs,
		sender:      cfg.Sender,
		store:       cfg.Pending,
		backlog:     cfg.Backlog,
		finder:      cfg.Finder,
		reviewStore: cfg.ReviewStore,
		validator:   cfg.Validator,
		debater:     cfg.Debater,
		logger:      cfg.Logger,
		newToken:    cfg.NewToken,
	}
}
