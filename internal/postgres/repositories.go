package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type repositories struct {
	sources          *sourceRepository
	knowledgeObjects *knowledgeObjectRepository
	objectSources    *objectSourceRepository
	auditEvents      *auditEventRepository
}

type objectValidationRepositories struct {
	objects     *knowledgeObjectRepository
	auditEvents *auditEventRepository
}

// debateRepositories is the bundle handed to a
// WithinObjectDebateTx callback. It deliberately mirrors
// objectValidationRepositories' shape (Objects + AuditEvents) but
// is a distinct type so the debate service cannot accidentally pick
// up a future method added only to the validation side. The
// underlying repository structs (knowledgeObjectRepository,
// auditEventRepository) are reused by value composition — the
// debate bundle does NOT introduce a parallel set of write-path
// repository structs.
type debateRepositories struct {
	objects     *knowledgeObjectRepository
	auditEvents *auditEventRepository
}

func newRepositories(tx pgx.Tx) *repositories {
	return &repositories{
		sources:          &sourceRepository{tx: tx},
		knowledgeObjects: &knowledgeObjectRepository{tx: tx},
		objectSources:    &objectSourceRepository{tx: tx},
		auditEvents:      &auditEventRepository{tx: tx},
	}
}

func newObjectValidationRepositories(tx pgx.Tx) *objectValidationRepositories {
	return &objectValidationRepositories{
		objects:     &knowledgeObjectRepository{tx: tx},
		auditEvents: &auditEventRepository{tx: tx},
	}
}

func newDebateRepositories(tx pgx.Tx) *debateRepositories {
	return &debateRepositories{
		objects:     &knowledgeObjectRepository{tx: tx},
		auditEvents: &auditEventRepository{tx: tx},
	}
}

func (r *repositories) Sources() app.SourceRepository                   { return r.sources }
func (r *repositories) KnowledgeObjects() app.KnowledgeObjectRepository { return r.knowledgeObjects }
func (r *repositories) ObjectSources() app.ObjectSourceRepository       { return r.objectSources }
func (r *repositories) AuditEvents() app.AuditEventRepository           { return r.auditEvents }

func (r *objectValidationRepositories) Objects() app.ObjectValidationObjectRepository {
	return r.objects
}
func (r *objectValidationRepositories) AuditEvents() app.AuditEventRepository { return r.auditEvents }

func (r *debateRepositories) Objects() app.ObjectDebateObjectRepository { return r.objects }
func (r *debateRepositories) AuditEvents() app.AuditEventRepository     { return r.auditEvents }

// Relations returns a standalone RelationRepository backed by its own connection.
func (db *DB) Relations() app.RelationRepository {
	return &relationRepository{conn: db.pool}
}

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
INSERT INTO knowledge_objects (id, workspace_id, type, title, summary, content, status, metadata, created_by, created_at, updated_at, project_id, tags, confidence, importance, language)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''))`,
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
		object.Language,
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

// marshalMetadata encodes Metadata for a JSONB column. A nil OR empty
// map becomes '{}'. The sources and knowledge_objects metadata columns
// are NOT NULL DEFAULT '{}'; writing an explicit SQL NULL violates the
// constraint (the DEFAULT only applies when the column is omitted from
// the INSERT, not when NULL is passed). So nil must serialize to an
// empty JSON object, not NULL — otherwise a metadata-less ingest fails
// with a 23502 not-null violation.
func marshalMetadata(metadata domain.Metadata) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
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

// nullableUUID returns the uuid pointer as-is; pgx maps a nil *uuid.UUID to
// SQL NULL. A non-nil pointer is passed through unchanged.
func nullableUUID(value *uuid.UUID) *uuid.UUID {
	return value
}

// nullableFloat64 returns the pointer as-is; pgx maps a nil *float64 to SQL
// NULL. A non-nil pointer is passed through unchanged.
func nullableFloat64(value *float64) *float64 {
	return value
}

// nullableInt returns the pointer as-is; pgx maps a nil *int to SQL NULL.
// A non-nil pointer is passed through unchanged.
func nullableInt(value *int) *int {
	return value
}

var _ app.SourceRepository = (*sourceRepository)(nil)
var _ app.KnowledgeObjectRepository = (*knowledgeObjectRepository)(nil)
var _ app.ObjectValidationObjectRepository = (*knowledgeObjectRepository)(nil)
var _ app.ObjectDebateObjectRepository = (*knowledgeObjectRepository)(nil)
var _ app.ObjectSourceRepository = (*objectSourceRepository)(nil)
var _ app.AuditEventRepository = (*auditEventRepository)(nil)
var _ app.IngestionRepositories = (*repositories)(nil)
var _ app.ObjectValidationRepositories = (*objectValidationRepositories)(nil)
var _ app.ObjectDebateRepositories = (*debateRepositories)(nil)
var _ app.RelationRepository = (*relationRepository)(nil)
var _ app.BacklogQuery = (*backlogQuery)(nil)

// relationRepository is a standalone repository for typed directed edges.
// Unlike the ingestion repositories, it operates on its own pgx connection
// rather than being bound to a transaction.
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

// backlogQuery is the read-side port implementation for the human
// backlog. It is a sibling of relationRepository — same pool-backed,
// not transactional — and a sibling of knowledgeObjectRepository
// (the read+write UoW-bounded surface) only in the sense that they
// read the same table. The backlog query is intentionally NOT
// added to a repositories{} bundle; it is exposed via
// newBacklogQuery(db.Pool()) so the service can be wired with just
// the read path and skip the UoW entirely on the GET.
type backlogQuery struct {
	conn *pgxpool.Pool
}

// newBacklogQuery returns a BacklogQuery backed by the given pool.
// The pool is the same one the write-path UoW uses; the read path
// does not need a dedicated pool because the query is short and
// the partial index idx_knowledge_objects_debating (migration
// 0012) keeps the debating subset O(workspace debating count).
func newBacklogQuery(db *pgxpool.Pool) *backlogQuery {
	return &backlogQuery{conn: db}
}

// NewBacklogQuery is the public composition-root factory. The
// lowercase newBacklogQuery is package-private so the implementation
// stays an internal detail; main.go (and any future caller) gets
// the app.BacklogQuery interface back. The name is singular
// ("BacklogQuery", not "NewBacklogQueryRepository") to match the
// app-layer port name and signal that this is a read-only,
// non-transactional surface.
func NewBacklogQuery(db *pgxpool.Pool) app.BacklogQuery {
	return newBacklogQuery(db)
}

// List runs the backlog query. The implementation:
//
//  1. Trims and lowercases the workspace ID (mirrors the service
//     layer's normalization; the service has already done this
//     but re-applying here keeps the repository safe to call
//     directly from a future HTTP handler without a service
//     round-trip).
//  2. Clamps the effective page size: 0 → BacklogDefaultPageSize,
//     >BacklogMaxPageSize → BacklogMaxPageSize. A value of 0 is
//     "use the default", NOT "return zero rows".
//  3. Decodes the cursor. A malformed cursor returns
//     ErrInvalidCursor and never reaches the SQL planner (this is
//     the same contract the service layer enforces; the
//     repository re-applies it for defense in depth so direct
//     callers of newBacklogQuery get the same guarantee).
//  4. Runs the SQL with LIMIT (pageSize + 1). The "fetch one
//     extra" pattern lets the service emit NextCursor exactly
//     when more rows exist without a second COUNT query.
//  5. Trims the trailing row so the service sees at most
//     pageSize items, with the (updated_at, id) of the last
//     row ready to encode as the next cursor.
//
// The query is served by the partial index
// idx_knowledge_objects_debating for the 'debating' subset; the
// 'proposed' and recent-'deprecated' portions fall back to a
// seqscan that is cheap in practice (per-workspace count is small
// for those subsets).
func (q *backlogQuery) List(ctx context.Context, filter app.BacklogFilter) (app.BacklogPage, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(filter.WorkspaceID))
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = app.BacklogDefaultPageSize
	}
	if pageSize > app.BacklogMaxPageSize {
		pageSize = app.BacklogMaxPageSize
	}

	cursor := strings.TrimSpace(filter.Cursor)
	var (
		cursorUpdatedAt time.Time
		cursorID        uuid.UUID
		hasCursor       bool
	)
	if cursor != "" {
		t, id, err := app.DecodeBacklogCursor(cursor)
		if err != nil {
			return app.BacklogPage{}, err
		}
		cursorUpdatedAt = t
		cursorID = id
		hasCursor = true
	}

	// SQL projection per the spec: filter by workspace + the
	// status mix (proposed OR debating OR recent-deprecated),
	// project the derived is_stale / stale_for_days columns,
	// order by (updated_at DESC, id DESC), and apply the keyset
	// cursor via a row-constructor comparison when present.
	// Fetching LIMIT (pageSize + 1) lets the service emit
	// NextCursor iff more rows exist.
	const backlogListQuery = `
SELECT
  id, workspace_id, type,
  COALESCE(title, '') AS title,
  COALESCE(summary, '') AS summary,
  status, updated_at,
  (status = 'debating' AND updated_at < now() - ($6::int * interval '1 day')) AS is_stale,
  GREATEST(EXTRACT(DAY FROM (now() - updated_at))::int, 0)         AS stale_for_days
FROM knowledge_objects
WHERE workspace_id = $1
  AND (
    status = 'proposed'
    OR status = 'debating'
    OR (status = 'deprecated' AND updated_at >= now() - ($7::int * interval '1 day'))
  )
  AND (
    $2::boolean = false
    OR (updated_at, id) < ($3::timestamptz, $4::uuid)
  )
ORDER BY updated_at DESC, id DESC
LIMIT $5`

	rows, err := q.conn.Query(ctx, backlogListQuery,
		workspaceID,
		hasCursor,
		cursorUpdatedAt,
		cursorID,
		pageSize+1,
		domain.DebateStaleDays,
		domain.BacklogRecentDeprecatedDays,
	)
	if err != nil {
		return app.BacklogPage{}, err
	}
	defer rows.Close()

	items := make([]app.BacklogItem, 0, pageSize+1)
	for rows.Next() {
		var item app.BacklogItem
		if err := rows.Scan(
			&item.ID,
			&item.WorkspaceID,
			&item.Type,
			&item.Title,
			&item.Summary,
			&item.Status,
			&item.UpdatedAt,
			&item.IsStale,
			&item.StaleForDays,
		); err != nil {
			return app.BacklogPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return app.BacklogPage{}, err
	}
	return app.BacklogPage{Items: items, NextCursor: ""}, nil
}
