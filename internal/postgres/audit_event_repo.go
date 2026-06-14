package postgres

import (
	"context"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Package postgres — audit_event_repo: implements app.AuditEventRepository.
// Moved from repositories.go in change-18 PR3. The struct is bound to a
// single pgx.Tx and is reused across all three UoW bundles (ingestion,
// objectValidation, debate) because the audit row is written alongside
// the state-change row in every UoW's commit. Sharing one struct is
// cheaper than maintaining parallel audit repos per UoW, and the
// interface signature is the same on every caller side.

type auditEventRepository struct {
	tx pgx.Tx
}

func (r *auditEventRepository) Create(ctx context.Context, event domain.AuditEvent) error {
	before, err := marshalMetadata(event.Before)
	if err != nil {
		return err
	}
	after, err := marshalMetadata(event.After)
	if err != nil {
		return err
	}
	metadata, err := marshalMetadata(event.Metadata)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
INSERT INTO audit_events (id, workspace_id, actor_id, action, target_type, target_id, before, after, reason, request_id, metadata, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10, $11::jsonb, $12)`,
		event.ID,
		event.WorkspaceID,
		nullableString(event.ActorID),
		event.Action,
		event.TargetType,
		event.TargetID,
		before,
		after,
		nullableString(event.Reason),
		nullableUUID(event.RequestID),
		metadata,
		event.CreatedAt,
	)
	return err
}

var _ app.AuditEventRepository = (*auditEventRepository)(nil)
