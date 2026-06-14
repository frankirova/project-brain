package postgres

import (
	"context"
	"errors"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Package postgres — knowledge_objects_repo: implements
// app.KnowledgeObjectRepository, app.ObjectValidationObjectRepository,
// app.ObjectDebateObjectRepository, and app.ObjectSourceRepository. Two
// structs, four interfaces, three UoW surfaces. Moved from
// repositories.go in change-18 PR3.
//
// `knowledgeObjectRepository` is bound to a single pgx.Tx created by
// WithinIngestionTx, WithinObjectValidationTx, or WithinObjectDebateTx
// depending on which UoW the service is calling through. The
// interface-set duplication is intentional: each UoW picks the methods
// it needs, so the same struct participates in the ingestion,
// validation, and debate UoWs without leaking the full method set to
// any one caller.
//
// `objectSourceRepository` lives in the same file because it is
// conceptually paired with the ingestion path: the link row references
// both the source and the knowledge_object created in the surrounding
// WithinIngestionTx callback and only makes sense in that context.

type knowledgeObjectRepository struct {
	tx pgx.Tx
}

func (r *knowledgeObjectRepository) Create(ctx context.Context, object domain.KnowledgeObject) error {
	metadata, err := marshalMetadata(object.Metadata)
	if err != nil {
		return err
	}
	// tags: pgx maps a Go []string directly to a Postgres TEXT[] column. We
	// never write nil (IngestTextService defaults it to []string{}), so the
	// NOT NULL DEFAULT '{}' constraint is satisfied.
	tags := object.Tags
	if tags == nil {
		tags = []string{}
	}
	_, err = r.tx.Exec(ctx, `
INSERT INTO knowledge_objects (id, workspace_id, type, title, summary, content, status, metadata, created_by, created_at, updated_at, project_id, tags, confidence, importance)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $13, $14, $15)`,
		object.ID,
		object.WorkspaceID,
		object.Type,
		nullableString(object.Title),
		nullableString(object.Summary),
		object.Content,
		object.Status,
		metadata,
		nullableString(object.CreatedBy),
		object.CreatedAt,
		object.UpdatedAt,
		nullableUUID(object.ProjectID),
		tags,
		nullableFloat64(object.Confidence),
		nullableInt(object.Importance),
	)
	return err
}

func (r *knowledgeObjectRepository) UpdateStatus(ctx context.Context, workspaceID string, id uuid.UUID, status string) error {
	tag, err := r.tx.Exec(ctx, `
UPDATE knowledge_objects
SET status = $3, updated_at = now()
WHERE workspace_id = $1 AND id = $2`,
		workspaceID, id, status,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return app.ErrNotFound
	}
	return nil
}

func (r *knowledgeObjectRepository) FindByIDForUpdate(ctx context.Context, workspaceID string, id uuid.UUID) (domain.KnowledgeObject, error) {
	const query = `
SELECT id, workspace_id, type, COALESCE(title, '') AS title,
       COALESCE(summary, '') AS summary, content, status, metadata,
       COALESCE(created_by, '') AS created_by, created_at, updated_at,
       project_id, tags, confidence, importance
FROM knowledge_objects
WHERE workspace_id = $1 AND id = $2
FOR UPDATE`
	var obj domain.KnowledgeObject
	err := r.tx.QueryRow(ctx, query, workspaceID, id).Scan(
		&obj.ID, &obj.WorkspaceID, &obj.Type, &obj.Title, &obj.Summary, &obj.Content,
		&obj.Status, &obj.Metadata, &obj.CreatedBy, &obj.CreatedAt, &obj.UpdatedAt,
		&obj.ProjectID, &obj.Tags, &obj.Confidence, &obj.Importance,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.KnowledgeObject{}, app.ErrNotFound
		}
		return domain.KnowledgeObject{}, err
	}
	return obj, nil
}

var _ app.KnowledgeObjectRepository = (*knowledgeObjectRepository)(nil)
var _ app.ObjectValidationObjectRepository = (*knowledgeObjectRepository)(nil)
var _ app.ObjectDebateObjectRepository = (*knowledgeObjectRepository)(nil)

// objectSourceRepository implements app.ObjectSourceRepository inside the
// WithinIngestionTx UoW boundary. Co-located with knowledgeObjectRepository
// because both are write-path repos that only make sense inside the
// ingestion callback; the link row references the surrounding
// knowledge_object by ID.
type objectSourceRepository struct {
	tx pgx.Tx
}

func (r *objectSourceRepository) Create(ctx context.Context, link domain.ObjectSource) error {
	_, err := r.tx.Exec(ctx, `
INSERT INTO object_sources (object_id, source_id, relevance)
VALUES ($1, $2, $3)`, link.ObjectID, link.SourceID, link.Relevance)
	return err
}

var _ app.ObjectSourceRepository = (*objectSourceRepository)(nil)
