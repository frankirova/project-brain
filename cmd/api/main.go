package main

import (
	"context"
	"fmt"
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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "open database: %v\n", err)
			os.Exit(1)
		}
		uow = db
		dbCloser = db.Close
	} else {
		uow = newInMemoryUOW()
		dbCloser = func() {}
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
		fmt.Printf("project-brain api listening port=%s environment=%s\n", cfg.Port, cfg.Environment)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "listen: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "telegram bot init: %v\n", err)
		} else {
			tgHandler.SetBot(b)
			go func() {
				fmt.Println("telegram bot starting (polling)")
				b.Start(ctx) // blocks until ctx is cancelled
			}()
		}
	} else {
		fmt.Println("telegram bot skipped (no PROJECT_BRAIN_TELEGRAM_BOT_TOKEN)")
	}

	// Wait for shutdown signal.
	<-ctx.Done()

	// Give in-flight requests up to the configured timeout to complete.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout())
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
	}

	dbCloser()
	fmt.Println("project-brain api stopped")
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
