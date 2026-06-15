package runtime

import (
	"context"
	"log/slog"
	"sync"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/postgres"
	"github.com/frankirova/project-brain/internal/telegram"
	tgbot "github.com/go-telegram/bot"
)

// TelegramBot is the wired Telegram bot bundle BuildTelegramBot
// returns. Wait tracks the polling goroutine the bot spawns; main
// passes it to RunShutdown so the goroutine is joined before the
// process exits. Cancel is the function main calls to signal the
// polling goroutine to stop (the goroutine exits when the supplied
// ctx is cancelled, so main's signal-context cancellation covers
// it; Cancel is kept nil here on purpose — wiring it would
// duplicate the signal-driven cancel main already owns).
type TelegramBot struct {
	Handler *telegram.Handler
	Bot     *tgbot.Bot
	Wait    *sync.WaitGroup
}

// BuildTelegramBot wires the Telegram bot and starts polling. Returns
// (nil, nil) when cfg.TelegramBotToken is empty — local dev and
// HTTP-only deployments. The typed-nil collision detector guard
// lives here: the handler's nil check (Config.Detector == nil)
// must see the interface nil, NOT a typed-nil pointer boxed in a
// non-nil interface. Losing this guard makes the handler panic on
// the first inbound message.
//
// Behavior is preserved byte-for-byte from the original
// cmd/api/main.go:288-378: the pending-validation store, the review-
// action store, and the raw-input repo are all wired to the
// Postgres pool when a Postgres backend is available; the startup
// sweep pass reaps expired rows for both stores; the same log
// lines fire with the same keys.
func BuildTelegramBot(ctx context.Context, svcs Services, cfg config.Config, logger *slog.Logger) (*TelegramBot, error) {
	if cfg.TelegramBotToken == "" {
		logger.Info("telegram bot skipped", slog.String("reason", "PROJECT_BRAIN_TELEGRAM_BOT_TOKEN unset"))
		return nil, nil
	}

	// Pending validations are durable in PostgreSQL (so a restart does
	// not invalidate every outstanding button) and fall back to the
	// in-memory store when the DB is unavailable. The handler itself
	// accepts nil and installs the fallback, but we wire the Postgres
	// store explicitly when the DB is open to make the wiring
	// observable here in the composition root.
	// pgStore holds the concrete *postgres.PendingValidationStore so
	// the composition root can also drive its SweepExpired GC pass on
	// startup; the handler keeps depending on the interface only.
	var tgStore app.PendingValidationStore
	var pgStore *postgres.PendingValidationStore
	var rawInputRepo app.RawInputRepository // nil when no postgres backend
	var tgReviewStore app.TelegramReviewActionStore
	var pgReviewStore *postgres.TelegramReviewActionStore
	if pgDB, ok := svcs.UoW.(*postgres.DB); ok && pgDB != nil {
		pgStore = postgres.NewPendingValidationStore(pgDB.Pool())
		tgStore = pgStore
		rawInputRepo = postgres.NewRawInputRepo(pgDB.Pool())
		// Reap rows that expired since the previous run. Stale
		// prompts are harmless to read (Take already filters
		// them out) but they would keep the table growing across
		// deploys, so the GC pass keeps the steady state small.
		reaped, err := pgStore.SweepExpired(ctx)
		if err != nil {
			logger.Warn("telegram pending validation sweep failed",
				slog.String("error", err.Error()))
		} else if reaped > 0 {
			logger.Info("telegram pending validation sweep reaped rows",
				slog.Int64("count", reaped))
		}

		pgReviewStore = postgres.NewTelegramReviewActionStore(pgDB.Pool())
		tgReviewStore = pgReviewStore
		reviewReaped, reviewErr := pgReviewStore.SweepExpired(ctx)
		if reviewErr != nil {
			logger.Warn("telegram review action sweep failed",
				slog.String("error", reviewErr.Error()))
		} else if reviewReaped > 0 {
			logger.Info("telegram review action sweep reaped rows",
				slog.Int64("count", reviewReaped))
		}
	}
	// Pass a true nil interface when no detector exists — handing a
	// typed-nil *app.CollisionDetector would make the handler's nil
	// check fail and panic on the first message.
	// backlogSvc satisfies both backlogLister and reviewDebator.
	// validateSvc and ftsRetriever are nil when no Postgres backend
	// is available; the handler answers "no disponible" for those
	// buttons rather than panicking.
	var tgHandler *telegram.Handler
	tgCfg := telegram.Config{
		Service:     svcs.IngestService,
		Detector:    svcs.CollisionDetector,
		RawInputs:   rawInputRepo,
		Bot:         nil, // wired lazily by DefaultHandler on the first update
		Pending:     tgStore,
		Backlog:     svcs.BacklogService,
		Finder:      svcs.FTSRetriever,
		ReviewStore: tgReviewStore,
		Validator:   svcs.ValidateService,
		Debater:     svcs.BacklogService, // *ObjectDebateService satisfies reviewDebator
		Logger:      logger,
	}
	// typed-nil interface guard: when CollisionDetector is the typed
	// nil pointer (*app.CollisionDetector)(nil), assigning it to
	// tgCfg.Detector (a collisionChecker interface) produces a
	// non-nil interface wrapping a nil pointer. The handler's nil
	// check (tgCfg.Detector == nil) would then fail and the first
	// message would panic. applyTypedNilDetectorGuard sets the
	// Config field to a true nil interface when the source pointer
	// is nil, so the handler's nil check takes the
	// disabled-detector path. The guard is its own function so the
	// unit test in typed_nil_test.go can lock the behavior in
	// isolation (without it, the only way to verify the guard would
	// be to inspect the unexported collisionChecker field of the
	// built Handler, which is not accessible from this package).
	applyTypedNilDetectorGuard(&tgCfg, svcs.CollisionDetector)
	var errTelegram error
	tgHandler, errTelegram = telegram.New(tgCfg)
	if errTelegram != nil {
		logger.Error("telegram handler init", slog.String("error", errTelegram.Error()))
	}
	b, err := tgbot.New(cfg.TelegramBotToken,
		tgbot.WithDefaultHandler(tgHandler.DefaultHandler()),
	)
	if err != nil {
		logger.Error("telegram bot init", slog.String("error", err.Error()))
		return nil, err
	}

	botWG := &sync.WaitGroup{}
	botWG.Add(1)
	go func() {
		defer botWG.Done()
		logger.Info("telegram bot starting", slog.String("mode", "polling"))
		b.Start(ctx) // blocks until ctx is cancelled
		logger.Info("telegram bot stopped")
	}()

	return &TelegramBot{Handler: tgHandler, Bot: b, Wait: botWG}, nil
}

// applyTypedNilDetectorGuard sets cfg.Detector to the
// collisionChecker equivalent of d: when d is the typed-nil
// pointer, the field is set to a true nil interface (not a
// typed-nil boxed in a non-nil interface); when d is non-nil, the
// field holds the pointer. The guard is its own function so the
// behavior is testable from this package (the collisionChecker
// type lives in the telegram package and is not exported, so the
// only way to verify the guard's effect on the Config field is
// to read it back via the public field and compare to nil).
func applyTypedNilDetectorGuard(cfg *telegram.Config, d *app.CollisionDetector) {
	if d == nil {
		cfg.Detector = nil
		return
	}
	cfg.Detector = d
}
