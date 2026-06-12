package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPostgresObjectValidationPersistsStatusAndAudit(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{name: "approve proposed object", target: domain.KnowledgeObjectStatusValidated},
		{name: "reject proposed object", target: domain.KnowledgeObjectStatusDeprecated},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			db := openIntegrationDB(t)
			workspaceID := "workspace-" + uuid.NewString()
			t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
			objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
			auditID := uuid.New()
			requestID := uuid.New()
			now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
			service := app.NewValidateObjectServiceWithDependencies(db, func() uuid.UUID { return auditID }, func() time.Time { return now })

			result, err := service.Validate(context.Background(), app.ValidateObjectRequest{
				WorkspaceID:  workspaceID,
				ObjectID:     objectID,
				TargetStatus: tt.target,
				ActorID:      "reviewer-1",
				Reason:       "human reviewed",
				RequestID:    &requestID,
			})
			if err != nil {
				t.Fatalf("Validate() returned error: %v", err)
			}
			if result.ObjectID != objectID || result.Status != tt.target || result.AuditEventID != auditID {
				t.Fatalf("result = %+v", result)
			}

			assertObjectStatus(t, db, workspaceID, objectID, tt.target)
			assertStatusChangedAudit(t, db, workspaceID, objectID, auditID, requestID, tt.target)
		})
	}
}

func TestPostgresObjectValidationRejectsWrongWorkspaceAndUnsupportedTransitions(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	otherWorkspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, otherWorkspaceID) })
	objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
	service := app.NewValidateObjectService(db)

	_, err := service.Validate(context.Background(), app.ValidateObjectRequest{WorkspaceID: otherWorkspaceID, ObjectID: objectID, TargetStatus: domain.KnowledgeObjectStatusDeprecated})
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("wrong workspace Validate() error = %v, want ErrNotFound", err)
	}
	assertObjectStatus(t, db, workspaceID, objectID, domain.KnowledgeObjectStatusProposed)
	assertAuditCount(t, db, workspaceID, 0)

	_, err = service.Validate(context.Background(), app.ValidateObjectRequest{WorkspaceID: workspaceID, ObjectID: objectID, TargetStatus: domain.KnowledgeObjectStatusDebating})
	if !errors.Is(err, app.ErrInvalidTransition) {
		t.Fatalf("debating target Validate() error = %v, want ErrInvalidTransition", err)
	}
	assertObjectStatus(t, db, workspaceID, objectID, domain.KnowledgeObjectStatusProposed)
	assertAuditCount(t, db, workspaceID, 0)

	nonProposedID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusValidated)
	_, err = service.Validate(context.Background(), app.ValidateObjectRequest{WorkspaceID: workspaceID, ObjectID: nonProposedID, TargetStatus: domain.KnowledgeObjectStatusDeprecated})
	if !errors.Is(err, app.ErrInvalidTransition) {
		t.Fatalf("non-proposed Validate() error = %v, want ErrInvalidTransition", err)
	}
	assertObjectStatus(t, db, workspaceID, nonProposedID, domain.KnowledgeObjectStatusValidated)
	assertAuditCount(t, db, workspaceID, 0)
}

func TestPostgresObjectValidationRollsBackWhenAuditInsertFails(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
	objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
	failure := errors.New("audit insert failed")
	service := app.NewValidateObjectServiceWithDependencies(&failingAuditValidationUOW{db: db, err: failure}, uuid.New, time.Now)

	_, err := service.Validate(context.Background(), app.ValidateObjectRequest{WorkspaceID: workspaceID, ObjectID: objectID, TargetStatus: domain.KnowledgeObjectStatusValidated})
	if !errors.Is(err, failure) {
		t.Fatalf("Validate() error = %v, want audit failure", err)
	}
	assertObjectStatus(t, db, workspaceID, objectID, domain.KnowledgeObjectStatusProposed)
	assertAuditCount(t, db, workspaceID, 0)
}

func seedKnowledgeObject(t *testing.T, db *DB, workspaceID string, status string) uuid.UUID {
	t.Helper()
	objectID := uuid.New()
	now := time.Now().UTC()
	_, err := db.pool.Exec(context.Background(), `
INSERT INTO knowledge_objects (id, workspace_id, type, title, summary, content, status, metadata, created_by, created_at, updated_at, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7, '{}'::jsonb, $8, $9, $10, '{}')`,
		objectID, workspaceID, domain.KnowledgeObjectTypeDocument, "Validation object", "", "content", status, "tester", now, now,
	)
	if err != nil {
		t.Fatalf("seed knowledge object: %v", err)
	}
	return objectID
}

func assertObjectStatus(t *testing.T, db *DB, workspaceID string, objectID uuid.UUID, want string) {
	t.Helper()
	var got string
	if err := db.pool.QueryRow(context.Background(), `SELECT status FROM knowledge_objects WHERE workspace_id = $1 AND id = $2`, workspaceID, objectID).Scan(&got); err != nil {
		t.Fatalf("query object status: %v", err)
	}
	if got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func assertStatusChangedAudit(t *testing.T, db *DB, workspaceID string, objectID uuid.UUID, auditID uuid.UUID, requestID uuid.UUID, targetStatus string) {
	t.Helper()
	var (
		actorID    string
		action     string
		targetType string
		targetID   uuid.UUID
		before     domain.Metadata
		after      domain.Metadata
		reason     string
		gotRequest uuid.UUID
	)
	err := db.pool.QueryRow(context.Background(), `
SELECT COALESCE(actor_id, ''), action, target_type, target_id, before, after, COALESCE(reason, ''), request_id
FROM audit_events
WHERE workspace_id = $1 AND id = $2`, workspaceID, auditID).Scan(&actorID, &action, &targetType, &targetID, &before, &after, &reason, &gotRequest)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if actorID != "reviewer-1" || action != domain.AuditActionKnowledgeStatusChanged || targetType != domain.AuditTargetKnowledgeObject || targetID != objectID {
		t.Fatalf("audit identity = actor:%q action:%q target:%q/%s", actorID, action, targetType, targetID)
	}
	if before["status"] != domain.KnowledgeObjectStatusProposed || after["status"] != targetStatus {
		t.Fatalf("audit before/after = %+v/%+v", before, after)
	}
	if reason != "human reviewed" || gotRequest != requestID {
		t.Fatalf("audit reason/request = %q/%s, want human reviewed/%s", reason, gotRequest, requestID)
	}
}

func assertAuditCount(t *testing.T, db *DB, workspaceID string, want int) {
	t.Helper()
	var got int
	if err := db.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id = $1`, workspaceID).Scan(&got); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if got != want {
		t.Fatalf("audit count = %d, want %d", got, want)
	}
}

type failingAuditValidationUOW struct {
	db  *DB
	err error
}

func (u *failingAuditValidationUOW) WithinObjectValidationTx(ctx context.Context, fn func(context.Context, app.ObjectValidationRepositories) error) error {
	tx, err := u.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	repos := &failingAuditValidationRepos{
		object: &knowledgeObjectRepository{tx: tx},
		audit:  &failingAuditRepo{err: u.err},
	}
	if err := fn(ctx, repos); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

type failingAuditValidationRepos struct {
	object *knowledgeObjectRepository
	audit  *failingAuditRepo
}

func (r *failingAuditValidationRepos) Objects() app.ObjectValidationObjectRepository { return r.object }
func (r *failingAuditValidationRepos) AuditEvents() app.AuditEventRepository         { return r.audit }

type failingAuditRepo struct {
	err error
}

func (r *failingAuditRepo) Create(context.Context, domain.AuditEvent) error { return r.err }
