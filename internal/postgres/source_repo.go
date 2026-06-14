package postgres

import (
	"context"
	"errors"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Package postgres — source_repo: implements app.SourceRepository inside
// the WithinIngestionTx UoW boundary. Moved from repositories.go in
// change-18 PR3. The struct is bound to a single pgx.Tx created by
// WithinIngestionTx; the same instance is reused across the four writes
// of the ingestion contract (Source → KnowledgeObject → ObjectSource →
// AuditEvent).

type sourceRepository struct {
	tx pgx.Tx
}

func (r *sourceRepository) FindIngestionResultByIdentityKey(ctx context.Context, workspaceID string, identityKey string) (domain.IngestTextResult, error) {
	// A source can be linked to many knowledge_objects (one source may
	// produce several derived objects/chunks in future phases). We pick
	// one canonical object per source by sorting on object_id. The
	// audit_event for that object is then the single matching ingestion
	// event. Without picking object_id first, the LIMIT 1 would be
	// non-deterministic across joins.
	const query = `
SELECT s.id, os.object_id, COALESCE(ae.id, '00000000-0000-0000-0000-000000000000'::uuid), s.checksum, s.identity_key
FROM (
	SELECT id, checksum, identity_key
	FROM sources
	WHERE workspace_id = $1 AND identity_key = $2
	ORDER BY id
	LIMIT 1
) s
JOIN object_sources os ON os.source_id = s.id
LEFT JOIN audit_events ae
  ON ae.target_type = 'knowledge_object'
 AND ae.target_id = os.object_id
 AND ae.action = 'knowledge.ingested'
ORDER BY os.object_id
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

var _ app.SourceRepository = (*sourceRepository)(nil)
