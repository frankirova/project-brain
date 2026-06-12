// Package inmem provides in-memory implementations of the repository
// interfaces for local development and smoke tests. Persistence is
// per-process: every restart loses every write.
//
// This package lives in its own subdir to keep it out of the
// production binary's path in a way that's easy to grep for and
// easy to gate behind a build tag in the future.
package inmem

import (
	"context"
	"sync"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// UOW is an in-memory UnitOfWork. The repositories are no-ops:
// Find* always returns ErrNotFound, Create* returns nil. The
// service's idempotency check therefore never short-circuits in
// this mode — every call goes through the full ingestion path,
// and "duplicate" is always false.
type UOW struct{}

func NewUOW() *UOW { return &UOW{} }

func (u *UOW) WithinIngestionTx(ctx context.Context, fn func(context.Context, app.IngestionRepositories) error) error {
	return fn(ctx, &repos{})
}

type repos struct{}

func (r *repos) Sources() app.SourceRepository                   { return &sourceRepo{} }
func (r *repos) KnowledgeObjects() app.KnowledgeObjectRepository { return &objectRepo{} }
func (r *repos) ObjectSources() app.ObjectSourceRepository       { return &linkRepo{} }
func (r *repos) AuditEvents() app.AuditEventRepository           { return &auditRepo{} }

type sourceRepo struct{}

func (r *sourceRepo) FindIngestionResultByIdentityKey(_ context.Context, _ string, _ string) (domain.IngestTextResult, error) {
	return domain.IngestTextResult{}, app.ErrNotFound
}

func (r *sourceRepo) Create(_ context.Context, _ domain.Source) error { return nil }

type objectRepo struct{}

func (r *objectRepo) Create(_ context.Context, _ domain.KnowledgeObject) error { return nil }
func (r *objectRepo) UpdateStatus(_ context.Context, _ string, _ uuid.UUID, _ string) error {
	return nil
}

type linkRepo struct{}

func (r *linkRepo) Create(_ context.Context, _ domain.ObjectSource) error { return nil }

type auditRepo struct{}

func (r *auditRepo) Create(_ context.Context, _ domain.AuditEvent) error { return nil }

// PendingValidationStore is the in-memory implementation of
// app.PendingValidationStore. It is the default for local development
// and for runs without PostgreSQL — it preserves the pre-persistence
// behavior of the Telegram handler (entries lost on restart, "no
// longer available" on a stale button). In production main.go wires
// the Postgres-backed implementation instead.
type PendingValidationStore struct {
	mu   sync.Mutex
	data map[string]app.PendingValidation
}

// NewPendingValidationStore returns an empty in-memory store.
func NewPendingValidationStore() *PendingValidationStore {
	return &PendingValidationStore{data: make(map[string]app.PendingValidation)}
}

// Save stores entry keyed by entry.Token, overwriting any prior entry
// for the same token. The handler generates a fresh UUID per prompt,
// so collisions are not expected, but the overwrite keeps the
// implementation total and simple.
func (s *PendingValidationStore) Save(_ context.Context, entry app.PendingValidation) error {
	s.mu.Lock()
	s.data[entry.Token] = entry
	s.mu.Unlock()
	return nil
}

// Take atomically loads and removes the entry for token. Returns
// app.ErrNotFound when the token is unknown, has already been
// consumed, or has expired — a button can only be acted on once, and a
// stale prompt must behave like "no longer available" rather than
// resurrecting a forgotten input.
func (s *PendingValidationStore) Take(_ context.Context, token string) (app.PendingValidation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.data[token]
	if !ok {
		return app.PendingValidation{}, app.ErrNotFound
	}
	delete(s.data, token)
	// Expired entries look the same as missing ones to the handler:
	// both end with "no longer available" and neither ingests.
	if !entry.ExpiresAt.IsZero() && !time.Now().Before(entry.ExpiresAt) {
		return app.PendingValidation{}, app.ErrNotFound
	}
	return entry, nil
}

var _ app.PendingValidationStore = (*PendingValidationStore)(nil)
