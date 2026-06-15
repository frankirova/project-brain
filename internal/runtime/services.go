package runtime

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/app/inmem"
	"github.com/frankirova/project-brain/internal/config"
	"github.com/frankirova/project-brain/internal/gemini"
	"github.com/frankirova/project-brain/internal/httpapi"
	"github.com/frankirova/project-brain/internal/postgres"
)

// Services is the wired service-layer bundle BuildServices returns.
// Each field is populated when its corresponding precondition holds
// (a Postgres backend for the read-path services, a Gemini key for
// the embedding path). Nil-tolerant defaults in the rest of the
// project let BuildServer / BuildTelegramBot skip endpoints that
// have no backing dependency.
type Services struct {
	// UoW is the persistence unit-of-work (postgres or in-memory).
	UoW app.IngestionUnitOfWork
	// IngestService is the write path every entry point funnels into.
	IngestService *app.IngestTextService
	// FTSRetriever, ObjectHandler, SearchHandler, CollisionDetector,
	// CollisionHandler, BacklogService, BacklogHandler,
	// ValidateService, SddDocumentService, SddDocumentHandler are
	// all populated when a Postgres backend is wired. They stay nil
	// in the in-memory UoW fallback (where the read paths have no
	// durable data to serve).
	FTSRetriever       *postgres.FTSRetriever
	ObjectHandler      http.Handler
	SearchHandler      http.Handler
	CollisionDetector  *app.CollisionDetector
	CollisionHandler   http.Handler
	BacklogService     *app.ObjectDebateService
	BacklogHandler     http.Handler
	ValidateService    *app.ValidateObjectService
	SddDocumentService *app.SddDocumentService
	SddDocumentHandler http.Handler
	// EmbeddingRetryDone is closed when the embedding retry worker
	// goroutine exits. nil when the worker is not wired (in-memory
	// mode or no Gemini key); RunShutdown skips the wait when nil.
	EmbeddingRetryDone <-chan struct{}
	// DBCloser releases the database. In the in-memory branch this
	// is a no-op.
	DBCloser func()
}

// BuildServices selects the UoW (postgres if DSN set, in-memory
// otherwise) and wires the service-layer dependencies. The in-memory
// UoW branch is intentionally not gated here — main calls
// EnforceInMemoryProductionGuard first; if production + in-memory
// is configured the process exits with a non-zero status BEFORE
// reaching this function.
//
// Behavior is preserved byte-for-byte from the original
// cmd/api/main.go:1-450: every slog call uses the same message
// string, key set, and ordering; the type-assertion to *postgres.DB
// gates the same set of dependent services; the embedder is shared
// between the write path (post-ingest hook) and the read path
// (composite retriever).
func BuildServices(ctx context.Context, cfg config.Config, logger *slog.Logger) (Services, error) {
	// Persistence selection: PostgreSQL if DSN set, in-memory fake if not.
	var uow app.IngestionUnitOfWork
	var dbCloser func()
	if cfg.DatabaseDSN != "" {
		db, err := postgres.Open(ctx, cfg.DatabaseDSN)
		if err != nil {
			logger.Error("open database", slog.String("error", err.Error()))
			return Services{}, err
		}
		uow = db
		dbCloser = db.Close
		logger.Info("postgres connection opened")
	} else {
		uow = inmem.NewUOW()
		dbCloser = func() {}
		// In-memory mode is useful for local dev and smoke tests, but
		// running it in production silently loses every write on restart.
		// Refuse to start in production with no DSN. main() enforces
		// this via EnforceInMemoryProductionGuard before calling
		// BuildServices, so we are guaranteed the process is not in
		// production here — the log is purely informational.
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

	return Services{
		UoW:                uow,
		IngestService:      svc,
		FTSRetriever:       ftsRetriever,
		ObjectHandler:      objectHandler,
		SearchHandler:      searchHandler,
		CollisionDetector:  collisionDetector,
		CollisionHandler:   collisionHandler,
		BacklogService:     backlogSvc,
		BacklogHandler:     backlogHandler,
		ValidateService:    validateSvc,
		SddDocumentService: nil, // intentionally not exposed; lifecycle stays in main via the handlers
		SddDocumentHandler: sddDocumentHandler,
		EmbeddingRetryDone: retryDone,
		DBCloser:           dbCloser,
	}, nil
}
