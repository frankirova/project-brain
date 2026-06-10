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
