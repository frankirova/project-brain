package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ValidateObjectService handles the proposed → validated / deprecated
// lifecycle transitions. After the UoW transaction commits it optionally fires
// a best-effort PostValidationHook (for validated) or PostDeprecationHook (for
// deprecated). Hook errors are logged and swallowed; they never affect the
// validation result.
type ValidateObjectService struct {
	uow                 ObjectValidationUnitOfWork
	ids                 IDGenerator
	now                 Clock
	postValidationHook  PostValidationHook
	postDeprecationHook PostDeprecationHook
	logger              *slog.Logger
}

// ValidateObjectRequest carries the caller-supplied fields for a single
// lifecycle transition.
type ValidateObjectRequest struct {
	WorkspaceID  string
	ObjectID     uuid.UUID
	TargetStatus string
	ActorID      string
	Reason       string
	RequestID    *uuid.UUID
}

// ValidateObjectResult is the trimmed result returned to the caller after a
// successful transition. It contains only the object ID, the new status, and
// the audit event ID — not the full KnowledgeObject — so the hook is the only
// consumer of the full object without widening this type.
type ValidateObjectResult struct {
	ObjectID     uuid.UUID
	Status       string
	AuditEventID uuid.UUID
}

// NewValidateObjectService returns a service with default clock/id dependencies
// and slog.Default() as the logger. Kept for callers that do not need to
// override dependencies.
func NewValidateObjectService(uow ObjectValidationUnitOfWork) *ValidateObjectService {
	return NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)
}

// NewValidateObjectServiceWithDependencies returns a service with explicit
// clock and ID-generator overrides (used in tests). The logger defaults to
// slog.Default().
func NewValidateObjectServiceWithDependencies(uow ObjectValidationUnitOfWork, ids IDGenerator, now Clock) *ValidateObjectService {
	return &ValidateObjectService{uow: uow, ids: ids, now: now, logger: slog.Default()}
}

// SetPostValidationHook wires a best-effort hook to fire after each successful
// proposed → validated transition, outside the UoW transaction. The hook
// receives the full KnowledgeObject captured inside the transaction so it has
// the authoritative post-transition state without a second round-trip.
func (s *ValidateObjectService) SetPostValidationHook(h PostValidationHook) {
	s.postValidationHook = h
}

// SetPostDeprecationHook wires a best-effort hook to fire after each successful
// proposed → deprecated transition, outside the UoW transaction.
func (s *ValidateObjectService) SetPostDeprecationHook(h PostDeprecationHook) {
	s.postDeprecationHook = h
}

// Validate executes the lifecycle transition described by req. It is the only
// write path for the proposed → {validated, deprecated} transitions.
//
// The four-write transaction contract (FindByIDForUpdate + UpdateStatus +
// AuditEvents.Create + result capture) is unchanged. The hook fires strictly
// after the transaction commits.
func (s *ValidateObjectService) Validate(ctx context.Context, req ValidateObjectRequest) (ValidateObjectResult, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(req.WorkspaceID))
	targetStatus := strings.TrimSpace(req.TargetStatus)
	if !isAllowedValidationTarget(targetStatus) {
		return ValidateObjectResult{}, ErrInvalidTransition
	}

	var result ValidateObjectResult
	// hookedObject is captured inside the transaction so the post-commit hook
	// receives the full, authoritative post-transition KnowledgeObject without
	// a second database round-trip. The pattern mirrors IngestTextService's
	// createdObject capture.
	var hookedObject domain.KnowledgeObject

	err := s.uow.WithinObjectValidationTx(ctx, func(ctx context.Context, repos ObjectValidationRepositories) error {
		object, err := repos.Objects().FindByIDForUpdate(ctx, workspaceID, req.ObjectID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrNotFound
			}
			return err
		}

		if object.Status != domain.KnowledgeObjectStatusProposed {
			return ErrInvalidTransition
		}

		if err := repos.Objects().UpdateStatus(ctx, workspaceID, req.ObjectID, targetStatus); err != nil {
			return err
		}

		auditEventID := s.ids()
		auditEvent := domain.AuditEvent{
			ID:          auditEventID,
			WorkspaceID: workspaceID,
			ActorID:     strings.TrimSpace(req.ActorID),
			Action:      domain.AuditActionKnowledgeStatusChanged,
			TargetType:  domain.AuditTargetKnowledgeObject,
			TargetID:    req.ObjectID,
			Before:      domain.Metadata{"status": object.Status},
			After:       domain.Metadata{"status": targetStatus},
			Reason:      strings.TrimSpace(req.Reason),
			RequestID:   req.RequestID,
			Metadata:    domain.Metadata{},
			CreatedAt:   s.now().UTC(),
		}
		if err := repos.AuditEvents().Create(ctx, auditEvent); err != nil {
			return err
		}

		// Capture the full object with the new status so the hook has an
		// authoritative snapshot without a post-commit re-fetch.
		hookedObject = object
		hookedObject.Status = targetStatus
		hookedObject.UpdatedAt = s.now().UTC()

		result = ValidateObjectResult{ObjectID: req.ObjectID, Status: targetStatus, AuditEventID: auditEventID}
		return nil
	})
	if err != nil {
		return ValidateObjectResult{}, err
	}

	// Dispatch the appropriate best-effort hook post-commit.
	switch targetStatus {
	case domain.KnowledgeObjectStatusValidated:
		if s.postValidationHook != nil {
			if hookErr := s.postValidationHook(ctx, hookedObject); hookErr != nil {
				s.logger.Warn("sdd post-validation hook failed",
					slog.String("workspace_id", workspaceID),
					slog.String("object_id", req.ObjectID.String()),
					slog.String("error", hookErr.Error()),
				)
			}
		}
	case domain.KnowledgeObjectStatusDeprecated:
		if s.postDeprecationHook != nil {
			if hookErr := s.postDeprecationHook(ctx, hookedObject); hookErr != nil {
				s.logger.Warn("sdd post-deprecation hook failed",
					slog.String("workspace_id", workspaceID),
					slog.String("object_id", req.ObjectID.String()),
					slog.String("error", hookErr.Error()),
				)
			}
		}
	}

	return result, nil
}

func isAllowedValidationTarget(status string) bool {
	switch status {
	case domain.KnowledgeObjectStatusValidated, domain.KnowledgeObjectStatusDeprecated:
		return true
	default:
		return false
	}
}
