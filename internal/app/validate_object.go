package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

type ValidateObjectService struct {
	uow ObjectValidationUnitOfWork
	ids IDGenerator
	now Clock
}

type ValidateObjectRequest struct {
	WorkspaceID  string
	ObjectID     uuid.UUID
	TargetStatus string
	ActorID      string
	Reason       string
	RequestID    *uuid.UUID
}

type ValidateObjectResult struct {
	ObjectID     uuid.UUID
	Status       string
	AuditEventID uuid.UUID
}

func NewValidateObjectService(uow ObjectValidationUnitOfWork) *ValidateObjectService {
	return NewValidateObjectServiceWithDependencies(uow, uuid.New, time.Now)
}

func NewValidateObjectServiceWithDependencies(uow ObjectValidationUnitOfWork, ids IDGenerator, now Clock) *ValidateObjectService {
	return &ValidateObjectService{uow: uow, ids: ids, now: now}
}

func (s *ValidateObjectService) Validate(ctx context.Context, req ValidateObjectRequest) (ValidateObjectResult, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(req.WorkspaceID))
	targetStatus := strings.TrimSpace(req.TargetStatus)
	if !isAllowedValidationTarget(targetStatus) {
		return ValidateObjectResult{}, ErrInvalidTransition
	}

	var result ValidateObjectResult
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

		result = ValidateObjectResult{ObjectID: req.ObjectID, Status: targetStatus, AuditEventID: auditEventID}
		return nil
	})
	if err != nil {
		return ValidateObjectResult{}, err
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
