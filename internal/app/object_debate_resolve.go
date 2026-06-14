package app

import (
	"context"
	"errors"
	"strings"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ResolveDebateRequest is the input to ResolveDebate. TargetStatus
// MUST be "validated" or "deprecated". The source status MUST be
// "debating"; any other source returns ErrInvalidTransition.
type ResolveDebateRequest struct {
	WorkspaceID  string
	ObjectID     uuid.UUID
	TargetStatus string
	ActorID      string
	Reason       string
	RequestID    *uuid.UUID
}

// ResolveDebateResult is the outcome of ResolveDebate. Two audit
// rows are written atomically with the status update:
// knowledge.status_changed and knowledge.debate_resolved, sharing
// ActorID, RequestID, Before, After, and Reason. AuditEventID
// identifies the debate_resolved row (the domain-specific event);
// the status_changed row's ID is intentionally not surfaced because
// callers almost always need the domain-specific event for
// correlation.
type ResolveDebateResult struct {
	ObjectID     uuid.UUID
	Status       string
	AuditEventID uuid.UUID
}

// ResolveDebate transitions a debating object to validated or
// deprecated and emits a knowledge.debate_resolved audit event. The
// target must be one of {validated, deprecated}; the source must be
// debating. Two audit rows are written atomically with the status
// update: knowledge.status_changed and knowledge.debate_resolved,
// sharing ActorID, RequestID, Before, After, and Reason.
//
// Returns ErrInvalidTransition when the target is not validated or
// deprecated, or when the source status is not debating. Returns
// ErrNotFound when the object does not exist. Audit insert
// failures roll back the status update.
func (s *ObjectDebateService) ResolveDebate(ctx context.Context, req ResolveDebateRequest) (ResolveDebateResult, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(req.WorkspaceID))
	targetStatus := strings.TrimSpace(req.TargetStatus)
	if !isAllowedResolveTarget(targetStatus) {
		return ResolveDebateResult{}, ErrInvalidTransition
	}

	var result ResolveDebateResult
	err := s.uow.WithinObjectDebateTx(ctx, func(ctx context.Context, repos ObjectDebateRepositories) error {
		object, err := repos.Objects().FindByIDForUpdate(ctx, workspaceID, req.ObjectID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrNotFound
			}
			return err
		}

		// ResolveDebate only accepts "debating" as the source. Any
		// other source — including a "proposed" object the human
		// wants to skip debating on — is rejected with
		// ErrInvalidTransition. Use MarkDebating to escalate a
		// proposed object first.
		if object.Status != domain.KnowledgeObjectStatusDebating {
			return ErrInvalidTransition
		}

		if err := repos.Objects().UpdateStatus(ctx, workspaceID, req.ObjectID, targetStatus); err != nil {
			return err
		}

		statusChangedID := s.ids()
		statusChanged := domain.AuditEvent{
			ID:          statusChangedID,
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
		if err := repos.AuditEvents().Create(ctx, statusChanged); err != nil {
			return err
		}

		debateResolvedID := s.ids()
		debateResolved := domain.AuditEvent{
			ID:          debateResolvedID,
			WorkspaceID: workspaceID,
			ActorID:     strings.TrimSpace(req.ActorID),
			Action:      domain.AuditActionKnowledgeDebateResolved,
			TargetType:  domain.AuditTargetKnowledgeObject,
			TargetID:    req.ObjectID,
			Before:      domain.Metadata{"status": object.Status},
			After:       domain.Metadata{"status": targetStatus},
			Reason:      strings.TrimSpace(req.Reason),
			RequestID:   req.RequestID,
			Metadata:    domain.Metadata{},
			CreatedAt:   s.now().UTC(),
		}
		if err := repos.AuditEvents().Create(ctx, debateResolved); err != nil {
			return err
		}

		result = ResolveDebateResult{
			ObjectID:     req.ObjectID,
			Status:       targetStatus,
			AuditEventID: debateResolvedID,
		}
		return nil
	})
	if err != nil {
		return ResolveDebateResult{}, err
	}
	return result, nil
}
