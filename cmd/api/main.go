package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/frankirova/project-brain/internal/httpapi"
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
		uow = newInMemoryUOW()
		dbCloser = func() {}
		logger.Warn("running with in-memory uow", slog.String("reason", "PROJECT_BRAIN_DATABASE_DSN unset"))
	}

	svc := app.NewIngestTextService(uow)
	handler := httpapi.NewIngestTextHandler(svc)

	mux := http.NewServeMux()
	mux.Handle("POST /v1/ingest-text", handler)
	mux.Handle("GET /v1/health", &httpapi.HealthHandler{})

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	go func() {
		logger.Info("http server starting",
			slog.String("port", cfg.Port),
			slog.String("environment", cfg.Environment))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	// Telegram bot: start polling only if token is configured.
	if cfg.TelegramBotToken != "" {
		tgHandler := telegram.NewHandler(svc, nil)
		b, err := tgbot.New(cfg.TelegramBotToken,
			tgbot.WithDefaultHandler(tgHandler.DefaultHandler()),
		)
		if err != nil {
			logger.Error("telegram bot init", slog.String("error", err.Error()))
		} else {
			tgHandler.SetBot(b)
			go func() {
				logger.Info("telegram bot starting", slog.String("mode", "polling"))
				b.Start(ctx) // blocks until ctx is cancelled
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

// inMemoryUOW is a minimal in-memory fake for development without PostgreSQL.
type inMemoryUOW struct{}

func newInMemoryUOW() *inMemoryUOW {
	return &inMemoryUOW{}
}

func (u *inMemoryUOW) WithinIngestionTx(ctx context.Context, fn func(context.Context, app.IngestionRepositories) error) error {
	// For in-memory mode, provide no-op repositories.
	repos := &noopRepos{}
	return fn(ctx, repos)
}

type noopRepos struct{}

func (r *noopRepos) Sources() app.SourceRepository                   { return &noopSourceRepo{} }
func (r *noopRepos) KnowledgeObjects() app.KnowledgeObjectRepository { return &noopObjectRepo{} }
func (r *noopRepos) ObjectSources() app.ObjectSourceRepository       { return &noopLinkRepo{} }
func (r *noopRepos) AuditEvents() app.AuditEventRepository           { return &noopAuditRepo{} }

type noopSourceRepo struct{}

func (r *noopSourceRepo) FindIngestionResultByIdentityKey(_ context.Context, _ string, _ string) (domain.IngestTextResult, error) {
	return domain.IngestTextResult{}, app.ErrNotFound
}

func (r *noopSourceRepo) Create(_ context.Context, _ domain.Source) error {
	return nil
}

type noopObjectRepo struct{}

func (r *noopObjectRepo) Create(_ context.Context, _ domain.KnowledgeObject) error {
	return nil
}

type noopLinkRepo struct{}

func (r *noopLinkRepo) Create(_ context.Context, _ domain.ObjectSource) error {
	return nil
}

type noopAuditRepo struct{}

func (r *noopAuditRepo) Create(_ context.Context, _ domain.AuditEvent) error {
	return nil
}
