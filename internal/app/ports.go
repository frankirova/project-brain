package app

import (
	"context"
	"errors"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")
var ErrInvalidTransition = errors.New("invalid transition")

type IngestionUnitOfWork interface {
	WithinIngestionTx(ctx context.Context, fn func(context.Context, IngestionRepositories) error) error
}

type ObjectValidationUnitOfWork interface {
	WithinObjectValidationTx(ctx context.Context, fn func(context.Context, ObjectValidationRepositories) error) error
}

// ObjectDebateUnitOfWork is the transactional boundary for the
// human-loop-orchestrator write path. It mirrors the
// ObjectValidationUnitOfWork shape but is a separate type so future
// debate-specific repository methods (e.g., a future "lock" or
// "audit-and-notify" method) can be added without leaking into the
// validation UoW. The transaction is owned by the postgres layer
// (see (*DB).WithinObjectDebateTx); the callback receives the
// debate-scoped repository bundle and MUST return an error to
// trigger rollback, or nil to commit.
type ObjectDebateUnitOfWork interface {
	WithinObjectDebateTx(ctx context.Context, fn func(context.Context, ObjectDebateRepositories) error) error
}

// IngestionRepositories bundles the four repository interfaces that
// participate in a single ingestion transaction: Source, KnowledgeObject,
// ObjectSource, and AuditEvent.
//
// Asymmetry note: RelationRepository is intentionally NOT part of this
// interface. Relations are created independently of the ingest use
// case — a separate change added them as a standalone repo, accessible
// via DB.Relations(). Keeping them out of the ingestion UoW prevents
// accidental coupling. See ROADMAP.md and the knowledge-relations
// change archive for the rationale.
type IngestionRepositories interface {
	Sources() SourceRepository
	KnowledgeObjects() KnowledgeObjectRepository
	ObjectSources() ObjectSourceRepository
	AuditEvents() AuditEventRepository
}

type ObjectValidationRepositories interface {
	Objects() ObjectValidationObjectRepository
	AuditEvents() AuditEventRepository
}

type ObjectValidationObjectRepository interface {
	FindByIDForUpdate(ctx context.Context, workspaceID string, id uuid.UUID) (domain.KnowledgeObject, error)
	UpdateStatus(ctx context.Context, workspaceID string, id uuid.UUID, status string) error
}

// ObjectDebateRepositories is the repository bundle passed to the
// WithinObjectDebateTx callback. It deliberately mirrors the shape
// of ObjectValidationRepositories (Objects + AuditEvents) but is a
// distinct type so the debate service cannot accidentally pick up a
// future method added only to the validation side.
type ObjectDebateRepositories interface {
	Objects() ObjectDebateObjectRepository
	AuditEvents() AuditEventRepository
}

// ObjectDebateObjectRepository is the debate-scoped read+write
// surface for knowledge_objects inside a WithinObjectDebateTx. The
// service uses:
//
//   - FindByIDForUpdate to lock the row and read the current
//     status (drives both the proposed→debating transition and the
//     debating→{validated,deprecated} transition, plus the
//     duplicate-detection branch of MarkDebating).
//   - UpdateStatus to flip the row to the new status.
//
// Both methods are reused verbatim from the validation surface; the
// debate interface is a separate type only to keep the two
// services' method sets from drifting.
type ObjectDebateObjectRepository interface {
	FindByIDForUpdate(ctx context.Context, workspaceID string, id uuid.UUID) (domain.KnowledgeObject, error)
	UpdateStatus(ctx context.Context, workspaceID string, id uuid.UUID, status string) error
}

type SourceRepository interface {
	FindIngestionResultByIdentityKey(ctx context.Context, workspaceID string, identityKey string) (domain.IngestTextResult, error)
	Create(ctx context.Context, source domain.Source) error
}

type KnowledgeObjectRepository interface {
	Create(ctx context.Context, object domain.KnowledgeObject) error
	// UpdateStatus mutates an existing object's lifecycle status. Returns
	// ErrNotFound if no row matches (workspace_id, id). Implementations
	// must also bump updated_at to now().
	UpdateStatus(ctx context.Context, workspaceID string, id uuid.UUID, status string) error
}

type ObjectSourceRepository interface {
	Create(ctx context.Context, link domain.ObjectSource) error
}

type AuditEventRepository interface {
	Create(ctx context.Context, event domain.AuditEvent) error
}

type RelationRepository interface {
	Create(ctx context.Context, relation domain.Relation) error
	FindBySourceObjectID(ctx context.Context, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error)
	FindByTargetObjectID(ctx context.Context, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error)
	FindByType(ctx context.Context, workspaceID string, relType domain.RelationType) ([]domain.Relation, error)
}

// PendingValidationTTL bounds how long a pending validation may sit
// unanswered in the store. After this window the entry is treated as
// gone by Take and reaped by the postgres-side SweepExpired pass. The
// constant is a process-wide default; callers can override per entry
// by setting PendingValidation.ExpiresAt explicitly.
const PendingValidationTTL = 24 * time.Hour

// TelegramReviewActionTTL bounds how long a rendered Telegram review
// action button may remain actionable. It intentionally mirrors the
// collision-validation TTL: old inline keyboards should fail closed and
// ask the user to refresh instead of mutating lifecycle state long after
// the backlog card was rendered.
const TelegramReviewActionTTL = 24 * time.Hour

const (
	TelegramReviewActionValidate  = "validate"
	TelegramReviewActionDeprecate = "deprecate"
	TelegramReviewActionDebate    = "debate"
	TelegramReviewActionSkip      = "skip"
)

// TelegramReviewAction is the server-side context behind an opaque
// Telegram callback payload (`rv:<token>`). The callback payload carries
// only Token; every trusted field needed by future Telegram rendering and
// callback handling is loaded from this store.
//
// ActorID is the Telegram user ID that the action was created for and
// ChatID scopes the button to the originating chat. WorkspaceID is kept
// explicit even though the MVP uses "default" so later multi-workspace
// mapping does not have to change the store contract. ExpectedStatus lets
// callback handling detect stale buttons before calling lifecycle services.
// NextCursor is used by future Skip/Next rendering and is intentionally
// opaque to the store.
type TelegramReviewAction struct {
	Token          string
	WorkspaceID    string
	ActorID        int64
	ChatID         int64
	ObjectID       uuid.UUID
	Action         string
	ExpectedStatus string
	NextCursor     string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// TelegramReviewActionStore is the durability boundary for Telegram
// backlog review buttons. Implementations MUST guarantee:
//
//   - Save followed by Take for the same token returns the same action
//     and then app.ErrNotFound on any subsequent Take.
//   - Take for an unknown, expired, or already-consumed token returns
//     app.ErrNotFound.
//   - SweepExpired deletes expired rows and returns the number removed.
//
// The store deliberately does not interpret Action or ExpectedStatus;
// those are adapter/app concerns in later PRs. This PR only establishes
// opaque-token persistence, TTL, and single-use semantics.
type TelegramReviewActionStore interface {
	Save(ctx context.Context, action TelegramReviewAction) error
	Take(ctx context.Context, token string) (TelegramReviewAction, error)
	SweepExpired(ctx context.Context) (int64, error)
}

// PendingValidation is a candidate input awaiting a human decision
// after a collision was detected. The token is the short string carried
// in Telegram's callback data; the request is the full ingest payload
// that rides only on the server side; the collision is the top hit used
// to render the prompt and to tell the human "what did this clash
// with?" on discard. The chat ID is stored alongside the token so the
// TTL/GC pass can attribute stale entries without joining against the
// source message. ExpiresAt is the absolute cutoff after which Take
// must behave as if the entry were never saved; a zero value means
// "no expiry" (used by tests; production callers should set it).
// RawInputID links this validation to its raw_inputs row; a zero UUID
// means no raw_input is associated (forward-compat: entries written
// before migration 0011 have no raw_input_id).
type PendingValidation struct {
	Token      string
	ChatID     int64
	Request    domain.IngestTextRequest
	Collision  Collision
	RawInputID uuid.UUID
	ExpiresAt  time.Time
}

// RawInputRepository is the durability boundary for the raw_inputs
// staging table. Implementations operate outside any ingestion
// transaction — all methods are best-effort and must never be called
// from within an IngestionUnitOfWork callback.
//
//   - Create inserts a new row with status="pending".
//   - SetPromoted atomically sets status="promoted", promoted_object_id,
//     and updated_at=now().
//   - SetDiscarded atomically sets status="discarded" and updated_at=now().
//   - SetCollisionSummary sets the collision_summary JSONB column; called
//     after collision detection returns hits, before the inline keyboard
//     is sent to the user.
type RawInputRepository interface {
	Create(ctx context.Context, ri domain.RawInput) error
	SetPromoted(ctx context.Context, id uuid.UUID, objectID uuid.UUID) error
	SetDiscarded(ctx context.Context, id uuid.UUID) error
	SetCollisionSummary(ctx context.Context, id uuid.UUID, summary domain.Metadata) error
}

// PendingValidationStore is the durability boundary for in-flight
// collision validations. Implementations MUST guarantee:
//
//   - Save followed by Take for the same token returns the same entry
//     and then app.ErrNotFound on any subsequent Take — i.e. Take is
//     load-and-delete so a button can be acted on at most once.
//   - Take for an unknown token returns app.ErrNotFound with no other
//     side effects.
//   - Take for an expired entry also returns app.ErrNotFound (TTL
//     enforcement: stale prompts must not be acted on, and the entry
//     is removed as a side effect so the row does not reappear on
//     a later Sweep).
//
// This mirrors the in-memory map the handler used pre-persistence:
// survive the same semantics, just durable across restarts.
type PendingValidationStore interface {
	Save(ctx context.Context, entry PendingValidation) error
	Take(ctx context.Context, token string) (PendingValidation, error)
}

// EmbeddingJob is one durable retry of a failed embedding generation.
// The composite identity is (ObjectID, Model): the same object can hold
// pending retries for distinct models if the deployment ever rolls a
// new embedder, and re-enqueueing the same pair is an upsert (no
// duplicates from a re-running hook). Attempts is the total number of
// times the system tried to embed — the hook's first failure stores 1,
// each worker retry that also fails bumps it by one.
type EmbeddingJob struct {
	ObjectID    uuid.UUID
	WorkspaceID string
	Model       string
	Attempts    int
	LastError   string
	NextRetryAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// EmbeddingJobRepository is the durability boundary for the embedding
// retry queue. Implementations MUST:
//
//   - Enqueue is an upsert keyed by (ObjectID, Model). A later Enqueue
//     for the same pair must overwrite Attempts / LastError /
//     NextRetryAt with the supplied values so a retry-aware hook can
//     either seed a fresh row or reset a failing one without leaking
//     duplicates.
//   - ClaimDue returns up to limit jobs whose NextRetryAt <= now, and
//     atomically pushes their NextRetryAt forward by a short lease so
//     a sibling worker (or this one re-tick) does not pick the same
//     row. Implementations on a single connection MAY return jobs in
//     ascending NextRetryAt order so the oldest backlog drains first.
//   - MarkFailed updates Attempts / LastError / NextRetryAt on the
//     identified row. ObjectID + Model identify it.
//   - Delete removes the row identified by (ObjectID, Model). A
//     missing row is not an error — the worker may have raced a
//     concurrent delete.
type EmbeddingJobRepository interface {
	Enqueue(ctx context.Context, job EmbeddingJob) error
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]EmbeddingJob, error)
	MarkFailed(ctx context.Context, objectID uuid.UUID, model string, attempts int, lastErr string, nextRetryAt time.Time) error
	Delete(ctx context.Context, objectID uuid.UUID, model string) error
}

// KnowledgeObjectFinder is the read-side port the embedding retry
// worker uses to rehydrate the object's text on each attempt. Kept
// separate from KnowledgeObjectRepository because that one is the
// transactional write surface; the worker reads outside any ingest tx.
// The Postgres FTSRetriever satisfies it as-is.
type KnowledgeObjectFinder interface {
	FindByID(ctx context.Context, workspaceID string, id uuid.UUID) (*domain.KnowledgeObject, error)
}

// BacklogDefaultPageSize is the effective page size when
// BacklogFilter.PageSize is 0 (or negative). The same value is
// enforced both in the app layer (for the empty-page contract) and
// in the postgres implementation (for SQL LIMIT clamping). Keeping
// both sides aligned prevents a "service says 25, SQL says 100"
// drift when the implementation is swapped (e.g., a future
// in-memory fake for PR 4 integration tests).
const BacklogDefaultPageSize = 25

// BacklogMaxPageSize is the hard upper bound for BacklogFilter.PageSize.
// Values above this are clamped to this constant before issuing
// the SQL LIMIT. The bound exists for the same reason MaxSearchLimit
// does: prevent a single HTTP request from selecting unbounded rows.
const BacklogMaxPageSize = 100

// BacklogFilter is the input to ListHumanBacklog. WorkspaceID is
// the tenant scope; PageSize is clamped to [1, BacklogMaxPageSize]
// (0 → BacklogDefaultPageSize, >BacklogMaxPageSize → BacklogMaxPageSize).
// Cursor is the opaque token returned by the prior page's
// NextCursor; an empty string means "first page". The service is
// responsible for decoding the cursor; the postgres layer does not
// parse it.
type BacklogFilter struct {
	WorkspaceID string
	PageSize    int
	Cursor      string
}

// BacklogItem is one row in the human backlog. The shape is a
// trimmed projection of KnowledgeObject — only the fields the
// human-facing UI needs to render a list row. IsStale is the
// derived (status='debating' AND updated_at < now() - 14d) marker
// per the spec; StaleForDays is the integer day count clamped to
// non-negative (rows with future updated_at return 0).
type BacklogItem struct {
	ID           uuid.UUID
	WorkspaceID  string
	Type         string
	Title        string
	Summary      string
	Status       string
	UpdatedAt    time.Time
	IsStale      bool
	StaleForDays int
}

// BacklogPage is the response from ListHumanBacklog. Items is the
// page of rows (length <= BacklogMaxPageSize). NextCursor is the
// opaque token the caller passes back to fetch the next page; an
// empty NextCursor means "no more pages" and the caller MUST stop
// paginating.
type BacklogPage struct {
	Items      []BacklogItem
	NextCursor string
}

// BacklogQuery is the read-side port for the human backlog. The
// implementation is the postgres layer (newBacklogQuery), but a
// fake is used in unit tests so the service can be exercised
// without a live database.
//
// Implementations MUST:
//
//   - Clamp the effective page size to [1, BacklogMaxPageSize]
//     with 0 defaulting to BacklogDefaultPageSize. A value of 0 is
//     NOT a "limit 0" (which would mean "return everything"); it
//     is "use the default".
//   - Return ErrInvalidCursor when the supplied cursor does not
//     decode as a valid backlog cursor. The check MUST happen
//     before any database read so a malformed cursor never hits
//     the SQL planner.
//   - Filter strictly by workspace_id. The backlog is workspace-
//     scoped with no cross-tenant leakage.
//   - Include rows whose status is 'proposed' or 'debating', plus
//     'deprecated' rows whose updated_at is within the last
//     domain.BacklogRecentDeprecatedDays.
//   - Project the derived is_stale / stale_for_days fields per
//     the spec SQL projection.
//   - Order by (updated_at DESC, id DESC) and apply a keyset
//     cursor using (updated_at, id) < (cursor_updated_at, cursor_id).
type BacklogQuery interface {
	List(ctx context.Context, filter BacklogFilter) (BacklogPage, error)
}
