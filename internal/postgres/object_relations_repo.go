package postgres

import (
	"context"
	"errors"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Package postgres — object_relations_repo: implements
// app.RelationRepository. Moved from repositories.go in change-18 PR3.
// relationRepository is a standalone repository for typed directed
// edges. Unlike the ingestion repositories, it operates on its own
// pgx connection (the pool) rather than being bound to a transaction
// — relations are written by a separate use case (the "link" service)
// and do not participate in the ingestion 4-write contract.
//
// DB.Relations() lives here, not in db.go, because db.go is the
// preserved UoW-boundary file (per the knowledge-core-ingestion spec)
// and the relations surface is not a UoW — it is a plain pool-backed
// read+write repo.

func (db *DB) Relations() app.RelationRepository {
	return &relationRepository{conn: db.pool}
}

type relationRepository struct {
	conn *pgxpool.Pool
}

func (r *relationRepository) Create(ctx context.Context, relation domain.Relation) error {
	if !domain.ValidateRelationType(relation.RelationType) {
		return errors.New("invalid relation type: " + string(relation.RelationType))
	}
	if relation.SourceObjectID == relation.TargetObjectID {
		return errors.New("source and target object must be different")
	}
	metadata, err := marshalMetadata(relation.Metadata)
	if err != nil {
		return err
	}
	_, err = r.conn.Exec(ctx, `
INSERT INTO relations (id, workspace_id, source_object_id, target_object_id, relation_type, confidence, evidence, metadata, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)`,
		relation.ID,
		relation.WorkspaceID,
		relation.SourceObjectID,
		relation.TargetObjectID,
		string(relation.RelationType),
		relation.Confidence,
		relation.Evidence,
		metadata,
		relation.CreatedAt,
	)
	return err
}

func (r *relationRepository) FindBySourceObjectID(ctx context.Context, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error) {
	rows, err := r.conn.Query(ctx, `
SELECT id, workspace_id, source_object_id, target_object_id, relation_type, confidence, evidence, metadata, created_at
FROM relations
WHERE workspace_id = $1 AND source_object_id = $2`, workspaceID, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (r *relationRepository) FindByTargetObjectID(ctx context.Context, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error) {
	rows, err := r.conn.Query(ctx, `
SELECT id, workspace_id, source_object_id, target_object_id, relation_type, confidence, evidence, metadata, created_at
FROM relations
WHERE workspace_id = $1 AND target_object_id = $2`, workspaceID, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (r *relationRepository) FindByType(ctx context.Context, workspaceID string, relType domain.RelationType) ([]domain.Relation, error) {
	rows, err := r.conn.Query(ctx, `
SELECT id, workspace_id, source_object_id, target_object_id, relation_type, confidence, evidence, metadata, created_at
FROM relations
WHERE workspace_id = $1 AND relation_type = $2`, workspaceID, string(relType))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func scanRelations(rows pgx.Rows) ([]domain.Relation, error) {
	var relations []domain.Relation
	for rows.Next() {
		var rel domain.Relation
		var relType string
		if err := rows.Scan(
			&rel.ID,
			&rel.WorkspaceID,
			&rel.SourceObjectID,
			&rel.TargetObjectID,
			&relType,
			&rel.Confidence,
			&rel.Evidence,
			&rel.Metadata,
			&rel.CreatedAt,
		); err != nil {
			return nil, err
		}
		rel.RelationType = domain.RelationType(relType)
		relations = append(relations, rel)
	}
	return relations, rows.Err()
}

var _ app.RelationRepository = (*relationRepository)(nil)
