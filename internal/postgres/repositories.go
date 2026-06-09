package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/jackc/pgx/v5"
)

type repositories struct {
	sources          *sourceRepository
	knowledgeObjects *knowledgeObjectRepository
	objectSources    *objectSourceRepository
	auditEvents      *auditEventRepository
}

func newRepositories(tx pgx.Tx) *repositories {
	return &repositories{
		sources:          &sourceRepository{tx: tx},
		knowledgeObjects: &knowledgeObjectRepository{tx: tx},
		objectSources:    &objectSourceRepository{tx: tx},
		auditEvents:      &auditEventRepository{tx: tx},
	}
}

func (r *repositories) Sources() app.SourceRepository                   { return r.sources }
func (r *repositories) KnowledgeObjects() app.KnowledgeObjectRepository { return r.knowledgeObjects }
func (r *repositories) ObjectSources() app.ObjectSourceRepository       { return r.objectSources }
func (r *repositories) AuditEvents() app.AuditEventRepository           { return r.auditEvents }

type sourceRepository struct {
	tx pgx.Tx
}

func (r *sourceRepository) FindIngestionResultByIdentityKey(ctx context.Context, workspaceID string, identityKey string) (domain.IngestTextResult, error) {
	const query = `
SELECT s.id, os.object_id, COALESCE(ae.id, '00000000-0000-0000-0000-000000000000'::uuid), s.checksum, s.identity_key
FROM sources s
JOIN object_sources os ON os.source_id = s.id
LEFT JOIN audit_events ae
  ON ae.target_type = 'knowledge_object'
 AND ae.target_id = os.object_id
 AND ae.action = 'knowledge.ingested'
WHERE s.workspace_id = $1 AND s.identity_key = $2
ORDER BY ae.created_at DESC NULLS LAST
LIMIT 1`

	var result domain.IngestTextResult
	if err := r.tx.QueryRow(ctx, query, workspaceID, identityKey).Scan(
		&result.SourceID,
		&result.ObjectID,
		&result.AuditEventID,
		&result.ContentChecksum,
		&result.IdentityKey,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.IngestTextResult{}, app.ErrNotFound
		}
		return domain.IngestTextResult{}, err
	}
	return result, nil
}

func (r *sourceRepository) Create(ctx context.Context, source domain.Source) error {
	metadata, err := marshalMetadata(source.Metadata)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
INSERT INTO sources (id, workspace_id, type, uri, external_id, title, checksum, identity_key, metadata, captured_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)`,
		source.ID,
		source.WorkspaceID,
		source.Type,
		nullableString(source.URI),
		nullableString(source.ExternalID),
		nullableString(source.Title),
		source.Checksum,
		source.IdentityKey,
		metadata,
		source.CapturedAt,
	)
	return err
}

type knowledgeObjectRepository struct {
	tx pgx.Tx
}

func (r *knowledgeObjectRepository) Create(ctx context.Context, object domain.KnowledgeObject) error {
	metadata, err := marshalMetadata(object.Metadata)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
INSERT INTO knowledge_objects (id, workspace_id, type, title, summary, content, status, metadata, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11)`,
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
	)
	return err
}

type objectSourceRepository struct {
	tx pgx.Tx
}

func (r *objectSourceRepository) Create(ctx context.Context, link domain.ObjectSource) error {
	_, err := r.tx.Exec(ctx, `
INSERT INTO object_sources (object_id, source_id, relevance)
VALUES ($1, $2, $3)`, link.ObjectID, link.SourceID, link.Relevance)
	return err
}

type auditEventRepository struct {
	tx pgx.Tx
}

func (r *auditEventRepository) Create(ctx context.Context, event domain.AuditEvent) error {
	after, err := marshalMetadata(event.After)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
INSERT INTO audit_events (id, workspace_id, actor_id, action, target_type, target_id, after, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)`,
		event.ID,
		event.WorkspaceID,
		nullableString(event.ActorID),
		event.Action,
		event.TargetType,
		event.TargetID,
		after,
		event.CreatedAt,
	)
	return err
}

func marshalMetadata(metadata domain.Metadata) ([]byte, error) {
	if metadata == nil {
		metadata = domain.Metadata{}
	}
	return json.Marshal(metadata)
}

// nullableString maps empty optional create fields to SQL NULL.
func nullableString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

var _ app.SourceRepository = (*sourceRepository)(nil)
var _ app.KnowledgeObjectRepository = (*knowledgeObjectRepository)(nil)
var _ app.ObjectSourceRepository = (*objectSourceRepository)(nil)
var _ app.AuditEventRepository = (*auditEventRepository)(nil)
var _ app.IngestionRepositories = (*repositories)(nil)
