package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/app/inmem"
	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/gemini"
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

	// Retrieval + embedding wiring. Built once so the embedder is shared
	// between the write path (post-ingest embedding hook) and the read
	// path (vector search). The search/object handlers are only created
	// when a Postgres backend is available.
	//   - Postgres only            -> FTS search.
	//   - Postgres + Gemini key    -> hybrid search (FTS + vector, RRF)
	//                                 and the embedding hook on ingest.
	var searchHandler http.Handler
	var objectHandler http.Handler
	var collisionHandler http.Handler
	// backlogHandler is the human-loop-orchestrator (change 14, PR 3)
	// read path. Wired only when a Postgres backend is available so
	// the in-memory UoW fallback does not silently serve empty pages.
	var backlogHandler http.Handler
	// sddDocumentHandler serves GET /v1/sdd-document. Wired only when a
	// Postgres backend is available; the in-memory UoW does not implement
	// SddDocumentRepository.
	var sddDocumentHandler http.Handler
	// collisionDetector is hoisted so the Telegram handler (built later)
	// can reuse it for the human-in-the-loop validation flow. Stays nil
	// when vector search is off.
	var collisionDetector *app.CollisionDetector
	// ftsRetriever, backlogSvc, and validateSvc are hoisted so the
	// Telegram composition root (built after this block) can inject
	// them into NewHandlerWithBacklogAndReview. All three stay nil
	// when no Postgres backend is available; the handler propagates
	// nils as "not configured" gracefully.
	var ftsRetriever *postgres.FTSRetriever
	var backlogSvc *app.ObjectDebateService
	var validateSvc *app.ValidateObjectService
	// retryDone is closed when the embedding retry worker goroutine
	// exits. nil when the worker is not wired (in-memory mode or no
	// Gemini key); shutdown blocks on it only when set.
	var retryDone <-chan struct{}
	if pgDB, ok := uow.(*postgres.DB); ok && pgDB != nil {
		ftsRetriever = postgres.NewFTSRetriever(pgDB.Pool())
		objectHandler = httpapi.NewObjectHandler(ftsRetriever)

		// Human backlog read path (change 14, PR 3). The query is
		// pool-backed; the service is wired in the same block so the
		// in-memory UoW branch (below) does not get a half-built
		// service. BacklogHandler is built unconditionally inside
		// the postgres branch because the service depends on the
		// pool even when there is no Gemini key.
		backlogSvc = app.NewObjectDebateService(pgDB, postgres.NewBacklogQuery(pgDB.Pool()))
		backlogHandler = httpapi.NewBacklogHandler(backlogSvc)
		validateSvc = app.NewValidateObjectService(pgDB)

		// SDD document write + read path. The service is built
		// directly on the *postgres.DB because the DB now satisfies
		// app.SddDocumentUnitOfWork (it exposes WithinSddDocumentTx
		// for the contended write path and SddDocuments() for the
		// pool-backed read path). The row-locked JSONB merge runs
		// inside WithinSddDocumentTx so concurrent appends on the
		// same workspace_id never lose entries.
		sddSvc := app.NewSddDocumentService(pgDB, time.Now, logger)
		sddDocumentHandler = httpapi.NewSddDocumentHandler(sddSvc)
		validateSvc.SetPostValidationHook(sddSvc.AppendValidatedObject)
		validateSvc.SetPostDeprecationHook(sddSvc.AppendValidatedObject)

		if cfg.GeminiAPIKey != "" {
			embedder := gemini.NewEmbedder(cfg.GeminiAPIKey)
			embeddingRepo := postgres.NewEmbeddingRepo(pgDB.Pool())
			embeddingJobs := postgres.NewEmbeddingJobRepo(pgDB.Pool())

			// Write path: embed every new object after commit. The
			// retry-aware hook stays best-effort for the ingest
			// path (errors are logged, ingest succeeds) but enqueues
			// a durable retry job on failure, so a Gemini outage no
			// longer leaves the object silently vector-less.
			svc.SetPostIngestHook(app.NewRetryAwareEmbeddingHook(embedder, embeddingRepo, embeddingJobs, logger))

			// Read path: fuse FTS + vector with Reciprocal Rank Fusion. The
			// FTS retriever doubles as the object hydrator for both paths.
			vectorRetriever := postgres.NewVectorRetriever(embedder, embeddingRepo, ftsRetriever, 0)
			composite := app.NewCompositeRetriever([]app.Retriever{ftsRetriever, vectorRetriever}, 0, 0)
			composite.SetHydrator(ftsRetriever)
			searchHandler = httpapi.NewSearchHandler(composite)

			// Collision detection: "what existing knowledge would this clash
			// with?" — embeds candidate text and returns similar objects.
			collisionDetector = app.NewCollisionDetector(embedder, embeddingRepo, ftsRetriever, 0, 0)
			collisionHandler = httpapi.NewCollisionHandler(collisionDetector, cfg.IngestMaxBytes)

			logger.Info("hybrid search + collision detection enabled",
				slog.String("provider", "gemini"),
				slog.String("model", embedder.Model()),
				slog.Int("dimensions", embedder.Dimensions()))

			// Background drain of the embedding retry queue. Reuses
			// the same FTSRetriever for object hydration on each
			// retry. The goroutine exits when ctx is cancelled by
			// the shutdown handler below.
			retryService := app.NewEmbeddingRetryService(
				embedder, embeddingRepo, embeddingJobs, ftsRetriever,
				logger, time.Now, 0,
			)
			retryDone = retryService.Start(ctx, 0)
		} else {
			searchHandler = httpapi.NewSearchHandler(ftsRetriever)
			logger.Info("search enabled (fts only)",
				slog.String("reason", "PROJECT_BRAIN_GEMINI_API_KEY unset; vector search off"))
		}
	} else {
		logger.Info("search + object endpoints disabled", slog.String("reason", "no postgres backend"))
	}

	handler := httpapi.NewIngestTextHandler(svc, cfg.IngestMaxBytes)

	// Public mux: only the health probe. No auth, no rate limit — health
	// must work even when the service is being abused or auth is broken.
	publicMux := http.NewServeMux()
	publicMux.Handle("GET /v1/health", &httpapi.HealthHandler{})

	// Protected mux: ingest endpoint goes through auth then rate limit.
	// Search and object endpoints are also protected (they read tenant
	// data). They are only registered when a retriever was built above.
	protectedMux := http.NewServeMux()
	protectedMux.Handle("POST /v1/ingest-text", handler)
	if searchHandler != nil {
		protectedMux.Handle("GET /v1/search", searchHandler)
		protectedMux.Handle("GET /v1/objects/{id}", objectHandler)
	}
	if collisionHandler != nil {
		protectedMux.Handle("POST /v1/check-collision", collisionHandler)
	}
	if backlogHandler != nil {
		protectedMux.Handle("GET /v1/backlog", backlogHandler)
	}
	if sddDocumentHandler != nil {
		protectedMux.Handle("GET /v1/sdd-document", sddDocumentHandler)
	}

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
	//
	// Transport timeouts. Load() already rejects non-positive values,
	// so under normal config the panic below is unreachable. It stays
	// as defense in depth: if a future refactor hands in a zero-value
	// Config (e.g. in a test), the server MUST NOT start with an
	// unprotected transport.
	readHeaderTimeout := cfg.HTTPReadHeaderTimeout()
	readTimeout := cfg.HTTPReadTimeout()
	writeTimeout := cfg.HTTPWriteTimeout()
	idleTimeout := cfg.HTTPIdleTimeout()
	if readHeaderTimeout <= 0 || readTimeout <= 0 || writeTimeout <= 0 || idleTimeout <= 0 {
		logger.Error("http transport timeouts must all be > 0",
			slog.Duration("read_header_timeout", readHeaderTimeout),
			slog.Duration("read_timeout", readTimeout),
			slog.Duration("write_timeout", writeTimeout),
			slog.Duration("idle_timeout", idleTimeout))
		panic("http transport timeouts must all be > 0")
	}
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           rootMux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
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
		if pgDB, ok := uow.(*postgres.DB); ok && pgDB != nil {
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
		if collisionDetector != nil {
			tgHandler = telegram.NewHandlerWithBacklogAndReview(svc, collisionDetector, rawInputRepo, nil, logger, tgStore, backlogSvc, ftsRetriever, tgReviewStore, validateSvc, backlogSvc)
		} else {
			tgHandler = telegram.NewHandlerWithBacklogAndReview(svc, nil, rawInputRepo, nil, logger, tgStore, backlogSvc, ftsRetriever, tgReviewStore, validateSvc, backlogSvc)
		}
		b, err := tgbot.New(cfg.TelegramBotToken,
			tgbot.WithDefaultHandler(tgHandler.DefaultHandler()),
		)
		if err != nil {
			logger.Error("telegram bot init", slog.String("error", err.Error()))
		} else {
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

	// Wait for the embedding retry worker to drain. It only exists
	// when Postgres + Gemini are both wired; otherwise retryDone is
	// nil and the select short-circuits.
	if retryDone != nil {
		select {
		case <-retryDone:
			logger.Info("embedding retry worker joined")
		case <-time.After(cfg.ShutdownTimeout()):
			logger.Warn("embedding retry worker did not exit before shutdown timeout")
		}
	}

	dbCloser()
	logger.Info("project-brain api stopped")
}

// newLogger returns a slog.Logger configured per environment and
// PROJECT_BRAIN_LOG_LEVEL override. Production logs are JSON for
// aggregation; development logs are text for readability.
func newLogger(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}
	if v := os.Getenv("PROJECT_BRAIN_LOG_LEVEL"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			level = slog.LevelDebug
		case "info":
			level = slog.LevelInfo
		case "warn", "warning":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		default:
			fmt.Fprintf(os.Stderr, "unknown PROJECT_BRAIN_LOG_LEVEL=%q, falling back to default\n", v)
		}
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
