package postgres

import (
	"context"
	"encoding/json"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RawInputRepo is the PostgreSQL implementation of app.RawInputRepository.
// It operates on its own pool connection — never inside an ingestion
// transaction — so Create/SetPromoted/SetDiscarded/SetCollisionSummary are
// all best-effort by contract.
type RawInputRepo struct {
	pool *pgxpool.Pool
}

// NewRawInputRepo returns a RawInputRepo backed by the given pool.
func NewRawInputRepo(pool *pgxpool.Pool) *RawInputRepo {
	return &RawInputRepo{pool: pool}
}

// Create inserts a new raw_input row with status="pending".
// external_ref uses marshalMetadata (nil → '{}'); collision_summary uses
// nullableJSONB (nil → SQL NULL, not '{}') because the column is nullable.
func (r *RawInputRepo) Create(ctx context.Context, ri domain.RawInput) error {
	extRef, err := marshalMetadata(ri.ExternalRef)
	if err != nil {
		return err
	}
	collSum, err := nullableJSONB(ri.CollisionSummary)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
INSERT INTO raw_inputs (id, workspace_id, channel, content, actor_id, external_ref, status, promoted_object_id, collision_summary, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9::jsonb, now(), now())`,
		ri.ID,
		ri.WorkspaceID,
		ri.Channel,
		ri.Content,
		nullableString(ri.ActorID),
		extRef,
		ri.Status,
		nullableUUID(ri.PromotedObjectID),
		collSum,
	)
	return err
}

// SetPromoted atomically transitions a raw_input to status="promoted"
// and records the created knowledge_object's ID.
func (r *RawInputRepo) SetPromoted(ctx context.Context, id uuid.UUID, objectID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
UPDATE raw_inputs
SET status = 'promoted', promoted_object_id = $2, updated_at = now()
WHERE id = $1`,
		id, objectID,
	)
	return err
}

// SetDiscarded atomically transitions a raw_input to status="discarded".
func (r *RawInputRepo) SetDiscarded(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
UPDATE raw_inputs
SET status = 'discarded', updated_at = now()
WHERE id = $1`,
		id,
	)
	return err
}

// SetCollisionSummary stores the top-collision data after the detector
// returns hits, before the inline keyboard is sent. Called best-effort:
// a failure is logged by the caller but never blocks the user response.
func (r *RawInputRepo) SetCollisionSummary(ctx context.Context, id uuid.UUID, summary domain.Metadata) error {
	data, err := nullableJSONB(summary)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
UPDATE raw_inputs
SET collision_summary = $2::jsonb, updated_at = now()
WHERE id = $1`,
		id, data,
	)
	return err
}

// nullableJSONB encodes Metadata for a nullable JSONB column.
// Unlike marshalMetadata, a nil input produces a real SQL NULL rather
// than '{}'. Use this for collision_summary which is genuinely nullable.
func nullableJSONB(m domain.Metadata) ([]byte, error) {
	if m == nil {
		return nil, nil // pgx maps nil []byte to SQL NULL
	}
	return json.Marshal(m)
}

// Compile-time interface check.
var _ app.RawInputRepository = (*RawInputRepo)(nil)
