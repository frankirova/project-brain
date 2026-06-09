package app

import (
	"context"
	"errors"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

var ErrNotFound = errors.New("not found")

type IngestionUnitOfWork interface {
	WithinIngestionTx(ctx context.Context, fn func(context.Context, IngestionRepositories) error) error
}

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
