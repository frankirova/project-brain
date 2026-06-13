package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

func TestValidateObjectAllowsProposedTerminalReviewTransitions(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{name: "approve proposed object", target: domain.KnowledgeObjectStatusValidated},
		{name: "reject proposed object", target: domain.KnowledgeObjectStatusDeprecated},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			auditID := uuid.MustParse("00000000-0000-0000-0000-000000000101")
			objectID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
			requestID := uuid.MustParse("00000000-0000-0000-0000-000000000301")
			now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
			uow := newFakeValidationUOW(domain.KnowledgeObject{
				ID:          objectID,
				WorkspaceID: "workspace-1",
				Status:      domain.KnowledgeObjectStatusProposed,
			})
			service := NewValidateObjectServiceWithDependencies(uow, func() uuid.UUID { return auditID }, func() time.Time { return now })

			result, err := service.Validate(context.Background(), ValidateObjectRequest{
				WorkspaceID:  " Workspace-1 ",
				ObjectID:     objectID,
				TargetStatus: tt.target,
				ActorID:      " reviewer-1 ",
				Reason:       " reviewed by human ",
				RequestID:    &requestID,
			})
			if err != nil {
				t.Fatalf("Validate() returned error: %v", err)
			}
			if !uow.committed || uow.rolledBack {
				t.Fatalf("transaction committed=%v rolledBack=%v, want committed only", uow.committed, uow.rolledBack)
			}
			if result.ObjectID != objectID || result.Status != tt.target || result.AuditEventID != auditID {
				t.Fatalf("result = %+v, want object/status/audit", result)
			}
			if uow.repos.object.updatedStatus != tt.target {
				t.Fatalf("updated status = %q, want %q", uow.repos.object.updatedStatus, tt.target)
			}

			audit := uow.repos.audit.created[0]
			if audit.ID != auditID || audit.WorkspaceID != "workspace-1" || audit.ActorID != "reviewer-1" {
				t.Fatalf("audit identity = %+v", audit)
			}
			if audit.Action != domain.AuditActionKnowledgeStatusChanged || audit.TargetType != domain.AuditTargetKnowledgeObject || audit.TargetID != objectID {
				t.Fatalf("audit target/action = %+v", audit)
			}
			if audit.Before["status"] != domain.KnowledgeObjectStatusProposed || audit.After["status"] != tt.target {
				t.Fatalf("audit before/after = %+v/%+v", audit.Before, audit.After)
			}
			if audit.Reason != "reviewed by human" || audit.RequestID == nil || *audit.RequestID != requestID || !audit.CreatedAt.Equal(now) {
				t.Fatalf("audit context = %+v", audit)
			}
		})
	}
}

func TestValidateObjectRejectsUnsupportedTargetsBeforeTransaction(t *testing.T) {
	for _, target := range []string{domain.KnowledgeObjectStatusDebating, domain.KnowledgeObjectStatusActive, domain.KnowledgeObjectStatusProposed, ""} {
		t.Run("target "+target, func(t *testing.T) {
			uow := newFakeValidationUOW(domain.KnowledgeObject{Status: domain.KnowledgeObjectStatusProposed})
			service := NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)

			_, err := service.Validate(context.Background(), ValidateObjectRequest{WorkspaceID: "workspace-1", ObjectID: uuid.New(), TargetStatus: target})
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("Validate() error = %v, want ErrInvalidTransition", err)
			}
			if uow.started || uow.repos.object.updatedStatus != "" || len(uow.repos.audit.created) != 0 {
				t.Fatalf("started=%v updated=%q audits=%d, want no writes", uow.started, uow.repos.object.updatedStatus, len(uow.repos.audit.created))
			}
		})
	}
}

func TestValidateObjectRejectsMissingOrNonProposedObjects(t *testing.T) {
	cases := []struct {
		name       string
		object     domain.KnowledgeObject
		findErr    error
		wantErr    error
		wantLookup bool
	}{
		{name: "missing object", findErr: ErrNotFound, wantErr: ErrNotFound},
		{name: "validated source", object: domain.KnowledgeObject{Status: domain.KnowledgeObjectStatusValidated}, wantErr: ErrInvalidTransition},
		{name: "deprecated source", object: domain.KnowledgeObject{Status: domain.KnowledgeObjectStatusDeprecated}, wantErr: ErrInvalidTransition},
		{name: "debating source", object: domain.KnowledgeObject{Status: domain.KnowledgeObjectStatusDebating}, wantErr: ErrInvalidTransition},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			uow := newFakeValidationUOW(tt.object)
			uow.repos.object.findErr = tt.findErr
			service := NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)

			_, err := service.Validate(context.Background(), ValidateObjectRequest{WorkspaceID: "workspace-1", ObjectID: uuid.New(), TargetStatus: domain.KnowledgeObjectStatusValidated})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.wantErr)
			}
			if !uow.rolledBack || uow.committed {
				t.Fatalf("transaction committed=%v rolledBack=%v, want rollback only", uow.committed, uow.rolledBack)
			}
			if uow.repos.object.updatedStatus != "" || len(uow.repos.audit.created) != 0 {
				t.Fatalf("updated=%q audits=%d, want no writes", uow.repos.object.updatedStatus, len(uow.repos.audit.created))
			}
		})
	}
}

func TestValidateObjectRollsBackWhenAuditFails(t *testing.T) {
	failure := errors.New("audit insert failed")
	uow := newFakeValidationUOW(domain.KnowledgeObject{ID: uuid.New(), WorkspaceID: "workspace-1", Status: domain.KnowledgeObjectStatusProposed})
	uow.repos.audit.err = failure
	service := NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.Validate(context.Background(), ValidateObjectRequest{WorkspaceID: "workspace-1", ObjectID: uow.repos.object.object.ID, TargetStatus: domain.KnowledgeObjectStatusValidated})
	if !errors.Is(err, failure) {
		t.Fatalf("Validate() error = %v, want audit failure", err)
	}
	if !uow.rolledBack || uow.committed {
		t.Fatalf("transaction committed=%v rolledBack=%v, want rollback only", uow.committed, uow.rolledBack)
	}
	if uow.repos.object.updatedStatus != domain.KnowledgeObjectStatusValidated {
		t.Fatalf("updatedStatus = %q, want attempted validated write before rollback signal", uow.repos.object.updatedStatus)
	}
}

type fakeValidationUOW struct {
	started    bool
	committed  bool
	rolledBack bool
	repos      *fakeValidationRepos
}

func newFakeValidationUOW(object domain.KnowledgeObject) *fakeValidationUOW {
	return &fakeValidationUOW{repos: &fakeValidationRepos{object: &fakeValidationObjectRepo{object: object}, audit: &fakeValidationAuditRepo{}}}
}

func (u *fakeValidationUOW) WithinObjectValidationTx(ctx context.Context, fn func(context.Context, ObjectValidationRepositories) error) error {
	u.started = true
	if err := fn(ctx, u.repos); err != nil {
		u.rolledBack = true
		return err
	}
	u.committed = true
	return nil
}

type fakeValidationRepos struct {
	object *fakeValidationObjectRepo
	audit  *fakeValidationAuditRepo
}

func (r *fakeValidationRepos) Objects() ObjectValidationObjectRepository { return r.object }
func (r *fakeValidationRepos) AuditEvents() AuditEventRepository         { return r.audit }

type fakeValidationObjectRepo struct {
	object        domain.KnowledgeObject
	findErr       error
	updatedStatus string
}

func (r *fakeValidationObjectRepo) FindByIDForUpdate(_ context.Context, _ string, _ uuid.UUID) (domain.KnowledgeObject, error) {
	if r.findErr != nil {
		return domain.KnowledgeObject{}, r.findErr
	}
	return r.object, nil
}

func (r *fakeValidationObjectRepo) UpdateStatus(_ context.Context, _ string, _ uuid.UUID, status string) error {
	r.updatedStatus = status
	return nil
}

type fakeValidationAuditRepo struct {
	err     error
	created []domain.AuditEvent
}

func (r *fakeValidationAuditRepo) Create(_ context.Context, event domain.AuditEvent) error {
	if r.err != nil {
		return r.err
	}
	r.created = append(r.created, event)
	return nil
}

// ---------------------------------------------------------------------------
// Phase 4.5 — hook tests
// ---------------------------------------------------------------------------

func TestValidateObject_PostValidationHookCalledWithFullObject(t *testing.T) {
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000401")
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	uow := newFakeValidationUOW(domain.KnowledgeObject{
		ID:          objectID,
		WorkspaceID: "ws-hook",
		Type:        domain.KnowledgeObjectTypeDecision,
		Title:       "T",
		Summary:     "S",
		Status:      domain.KnowledgeObjectStatusProposed,
	})
	svc := NewValidateObjectServiceWithDependencies(uow, uuid.New, func() time.Time { return now })

	var hookObj domain.KnowledgeObject
	svc.SetPostValidationHook(func(_ context.Context, obj domain.KnowledgeObject) error {
		hookObj = obj
		return nil
	})

	result, err := svc.Validate(context.Background(), ValidateObjectRequest{
		WorkspaceID:  "ws-hook",
		ObjectID:     objectID,
		TargetStatus: domain.KnowledgeObjectStatusValidated,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if result.Status != domain.KnowledgeObjectStatusValidated {
		t.Fatalf("result.Status = %q, want validated", result.Status)
	}
	if hookObj.ID != objectID {
		t.Errorf("hook received object ID %v, want %v", hookObj.ID, objectID)
	}
	if hookObj.Status != domain.KnowledgeObjectStatusValidated {
		t.Errorf("hook object.Status = %q, want validated", hookObj.Status)
	}
	if !hookObj.UpdatedAt.Equal(now.UTC()) {
		t.Errorf("hook object.UpdatedAt = %v, want %v", hookObj.UpdatedAt, now.UTC())
	}
}

func TestValidateObject_HookErrorSwallowed(t *testing.T) {
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000402")
	uow := newFakeValidationUOW(domain.KnowledgeObject{
		ID: objectID, WorkspaceID: "ws-1", Status: domain.KnowledgeObjectStatusProposed,
	})
	svc := NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)
	svc.SetPostValidationHook(func(_ context.Context, _ domain.KnowledgeObject) error {
		return errors.New("hook failure")
	})

	_, err := svc.Validate(context.Background(), ValidateObjectRequest{
		WorkspaceID:  "ws-1",
		ObjectID:     objectID,
		TargetStatus: domain.KnowledgeObjectStatusValidated,
	})
	if err != nil {
		t.Errorf("Validate() returned error = %v, want nil (hook error swallowed)", err)
	}
}

func TestValidateObject_NilHookNoPanic(t *testing.T) {
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000403")
	uow := newFakeValidationUOW(domain.KnowledgeObject{
		ID: objectID, WorkspaceID: "ws-1", Status: domain.KnowledgeObjectStatusProposed,
	})
	svc := NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)
	// No hook set — must not panic.
	_, err := svc.Validate(context.Background(), ValidateObjectRequest{
		WorkspaceID:  "ws-1",
		ObjectID:     objectID,
		TargetStatus: domain.KnowledgeObjectStatusValidated,
	})
	if err != nil {
		t.Errorf("Validate() returned error = %v, want nil", err)
	}
}

func TestValidateObject_DeprecatedTargetFiresPostDeprecationHook(t *testing.T) {
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000404")
	uow := newFakeValidationUOW(domain.KnowledgeObject{
		ID: objectID, WorkspaceID: "ws-1", Status: domain.KnowledgeObjectStatusProposed,
	})
	svc := NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)

	var depHookCalled bool
	var valHookCalled bool
	svc.SetPostDeprecationHook(func(_ context.Context, _ domain.KnowledgeObject) error {
		depHookCalled = true
		return nil
	})
	svc.SetPostValidationHook(func(_ context.Context, _ domain.KnowledgeObject) error {
		valHookCalled = true
		return nil
	})

	_, err := svc.Validate(context.Background(), ValidateObjectRequest{
		WorkspaceID:  "ws-1",
		ObjectID:     objectID,
		TargetStatus: domain.KnowledgeObjectStatusDeprecated,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !depHookCalled {
		t.Error("post-deprecation hook was not called")
	}
	if valHookCalled {
		t.Error("post-validation hook must not fire for deprecated target")
	}
}

func TestValidateObject_ValidatedTargetFiresPostValidationHook(t *testing.T) {
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000405")
	uow := newFakeValidationUOW(domain.KnowledgeObject{
		ID: objectID, WorkspaceID: "ws-1", Status: domain.KnowledgeObjectStatusProposed,
	})
	svc := NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)

	var valHookCalled bool
	var depHookCalled bool
	svc.SetPostValidationHook(func(_ context.Context, _ domain.KnowledgeObject) error {
		valHookCalled = true
		return nil
	})
	svc.SetPostDeprecationHook(func(_ context.Context, _ domain.KnowledgeObject) error {
		depHookCalled = true
		return nil
	})

	_, err := svc.Validate(context.Background(), ValidateObjectRequest{
		WorkspaceID:  "ws-1",
		ObjectID:     objectID,
		TargetStatus: domain.KnowledgeObjectStatusValidated,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !valHookCalled {
		t.Error("post-validation hook was not called")
	}
	if depHookCalled {
		t.Error("post-deprecation hook must not fire for validated target")
	}
}
