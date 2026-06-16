package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/migrations"
	"github.com/frankirova/project-brain/internal/postgres"
	"github.com/frankirova/project-brain/internal/runtime"
	"github.com/pressly/goose/v3"
)

// main is the cmd/api composition shell. The full lifecycle
// (services, HTTP server, Telegram bot, shutdown) lives in
// internal/runtime; this function owns bootstrap, the two
// fail-closed production invariants, the signal lifecycle, and
// starting the HTTP server.
func main() {
	logger := runtime.NewLogger(os.Getenv("PROJECT_BRAIN_ENV"))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Fail-closed invariants (change-16, change-19): production must
	// boot with both an auth token and a real database. Both run
	// BEFORE BuildServices so a misconfigured production deploy
	// never reaches service construction.
	if err := enforceProductionAuth(cfg, logger); err != nil {
		os.Exit(1)
	}
	if err := runtime.EnforceInMemoryProductionGuard(cfg, logger); err != nil {
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Implements spec db-migrations §Auto-Apply on Startup + §Fail-Fast on Migration Error.
	// One pool, used for both the migration runner and the service
	// layer — see openspec/changes/embedded-migration-runner/design.md
	// for the startup sequence diagram. Ordering invariant:
	// pgxpool.New < migrations.Run < BuildServicesWithPool.
	pgDB, err := postgres.OpenWithLogger(ctx, cfg.DatabaseDSN, logger)
	if err != nil {
		logger.Error("open database", slog.String("error", err.Error()))
		os.Exit(1)
	}
	pool := pgDB.Pool()

	if err := migrations.Run(ctx, pool, migrations.FS); err != nil {
		// Best-effort: surface the failing version when goose wrapped
		// the error in a *goose.PartialError. Otherwise "unknown" —
		// the error message itself still names the file.
		var pe *goose.PartialError
		if errors.As(err, &pe) && pe.Failed != nil && pe.Failed.Source != nil {
			logger.Error("migrations failed",
				slog.Int64("version", pe.Failed.Source.Version),
				slog.String("err", err.Error()),
			)
		} else {
			logger.Error("migrations failed",
				slog.String("version", "unknown"),
				slog.String("err", err.Error()),
			)
		}
		os.Exit(1)
	}

	svcs, err := runtime.BuildServicesWithPool(ctx, &cfg, pool, logger)
	if err != nil {
		os.Exit(1)
	}

	server, err := runtime.BuildServer(svcs, cfg, logger)
	if err != nil {
		os.Exit(1)
	}

	go func() {
		logger.Info("http server starting",
			slog.String("port", cfg.Port),
			slog.String("environment", cfg.Environment))
		if err := server.HTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", slog.String("error", err.Error()))
			cancel() // trigger the shutdown path so the deferred dbCloser runs
		}
	}()

	bot, _ := runtime.BuildTelegramBot(ctx, svcs, cfg, logger)

	<-ctx.Done()
	logger.Info("shutdown signal received")

	var botWG *sync.WaitGroup
	if bot != nil {
		botWG = bot.Wait
	}
	steps := runtime.ComposeShutdownSteps(cfg, logger, server.HTTP, botWG, svcs.EmbeddingRetryDone, svcs.DBCloser)
	if err := runtime.RunShutdown(context.Background(), steps, logger); err != nil {
		logger.Error("shutdown", slog.String("error", err.Error()))
	}
	logger.Info("project-brain api stopped")
}
