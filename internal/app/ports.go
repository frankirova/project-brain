package app

import (
	"context"
	"errors"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")

type IngestionUnitOfWork interface {
	WithinIngestionTx(ctx context.Context, fn func(context.Context, IngestionRepositories) error) error
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
type PendingValidation struct {
	Token     string
	ChatID    int64
	Request   domain.IngestTextRequest
	Collision Collision
	ExpiresAt time.Time
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
