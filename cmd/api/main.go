package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/app/inmem"
	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/httpapi"
	"github.com/frankirova/project-brain/internal/httpapi/auth"
	"github.com/frankirova/project-brain/internal/httpapi/ratelimit"
	"github.com/frankirova/project-brain/internal/postgres"
	"github.com/frankirova/project-brain/internal/telegram"
	tgbot "github.com/go-telegram/bot"
)

func main() {
	logger := newLogger(os.Getenv("PROJECT_BRAIN_ENV"))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Persistence selection: PostgreSQL if DSN set, in-memory fake if not.
	var uow app.IngestionUnitOfWork
	var dbCloser func()
	if cfg.DatabaseDSN != "" {
		db, err := postgres.Open(ctx, cfg.DatabaseDSN)
		if err != nil {
			logger.Error("open database", slog.String("error", err.Error()))
			os.Exit(1)
		}
		uow = db
		dbCloser = db.Close
		logger.Info("postgres connection opened")
	} else {
		uow = inmem.NewUOW()
		dbCloser = func() {}
		// In-memory mode is useful for local dev and smoke tests, but
		// running it in production silently loses every write on restart.
		// Refuse to start in production with no DSN.
		if cfg.Environment == "production" {
			logger.Error("in-memory uow refused in production",
				slog.String("reason", "PROJECT_BRAIN_DATABASE_DSN unset"))
			os.Exit(1)
		}
		logger.Warn("running with in-memory uow", slog.String("reason", "PROJECT_BRAIN_DATABASE_DSN unset"))
	}

	svc := app.NewIngestTextService(uow)
	handler := httpapi.NewIngestTextHandler(svc)

	// Public mux: only the health probe. No auth, no rate limit — health
	// must work even when the service is being abused or auth is broken.
	publicMux := http.NewServeMux()
	publicMux.Handle("GET /v1/health", &httpapi.HealthHandler{})

	// Protected mux: ingest endpoint goes through auth then rate limit.
	protectedMux := http.NewServeMux()
	protectedMux.Handle("POST /v1/ingest-text", handler)

	limiter := ratelimit.New(cfg.RateLimitRPS, cfg.RateLimitBurst, 10*time.Minute)
	limiter.SetTrustProxy(cfg.TrustProxy)
	logger.Info("rate limit enabled",
		slog.Float64("rps", cfg.RateLimitRPS),
		slog.Float64("burst", cfg.RateLimitBurst),
		slog.Bool("trust_proxy", cfg.TrustProxy))

	if cfg.AuthToken == "" {
		logger.Warn("auth disabled", slog.String("reason", "PROJECT_BRAIN_AUTH_TOKEN unset"))
	} else {
		logger.Info("auth enabled", slog.String("scheme", "bearer"))
	}

	// Compose: top-level mux routes /v1/health to public, everything else
	// to the protected chain (auth -> rate limit -> handler).
	rootMux := http.NewServeMux()
	rootMux.Handle("GET /v1/health", publicMux)
	rootMux.Handle("/", auth.Middleware(cfg.AuthToken)(limiter.Middleware(protectedMux)))

	// Order: auth first, then rate limit, then handler. Rate limit runs
	// after auth so unauthenticated floods don't consume buckets.
	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: rootMux,
	}

	go func() {
		logger.Info("http server starting",
			slog.String("port", cfg.Port),
			slog.String("environment", cfg.Environment))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", slog.String("error", err.Error()))
			// Trigger the shutdown path instead of os.Exit — the
			// deferred dbCloser() needs to run.
			cancel()
		}
	}()

	// Telegram bot: start polling only if token is configured.
	var botWG sync.WaitGroup
	if cfg.TelegramBotToken != "" {
		tgHandler := telegram.NewHandler(svc, nil, logger)
		b, err := tgbot.New(cfg.TelegramBotToken,
			tgbot.WithDefaultHandler(tgHandler.DefaultHandler()),
		)
		if err != nil {
			logger.Error("telegram bot init", slog.String("error", err.Error()))
		} else {
			tgHandler.SetBot(b)
			botWG.Add(1)
			go func() {
				defer botWG.Done()
				logger.Info("telegram bot starting", slog.String("mode", "polling"))
				b.Start(ctx) // blocks until ctx is cancelled
				logger.Info("telegram bot stopped")
			}()
		}
	} else {
		logger.Info("telegram bot skipped", slog.String("reason", "PROJECT_BRAIN_TELEGRAM_BOT_TOKEN unset"))
	}

	// Wait for shutdown signal.
	<-ctx.Done()
	logger.Info("shutdown signal received")

	// Give in-flight requests up to the configured timeout to complete.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout())
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", slog.String("error", err.Error()))
	}

	// Wait for the Telegram polling goroutine to exit. b.Start blocks
	// until ctx is cancelled, so this should be near-instant.
	botDone := make(chan struct{})
	go func() { botWG.Wait(); close(botDone) }()
	select {
	case <-botDone:
		logger.Info("telegram bot goroutine joined")
	case <-time.After(cfg.ShutdownTimeout()):
		logger.Warn("telegram bot goroutine did not exit before shutdown timeout")
	}

	dbCloser()
	logger.Info("project-brain api stopped")
}

// newLogger returns a slog.Logger configured per environment. Production
// uses JSON for log aggregation; development uses text for readability.
func newLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if env == "production" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
