package app

import (
	"context"
	"errors"
	"strings"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// MarkDebatingRequest is the input to MarkDebating. TriggeredBy
// discriminates the dual-initiator model:
//
//   - "system" — a bot (e.g., the contradiction detector) suggested
//     the debate. SuggestedBy MUST be set to a well-known bot
//     identifier (currently DebateSuggestionContradictionDetector);
//     the resulting audit row carries Metadata.suggested_by.
//   - "human"  — a human called the service directly. SuggestedBy
//     MUST be empty; Metadata.suggested_by is omitted from the
//     audit row.
//
// The transition itself is always a human authorization; humans
// close the debate loop. TriggeredBy captures only who initiated
// the suggestion, never who approved it.
type MarkDebatingRequest struct {
	WorkspaceID string
	ObjectID    uuid.UUID
	TriggeredBy string
	SuggestedBy string
	ActorID     string
	Reason      string
	RequestID   *uuid.UUID
}

// MarkDebatingResult is the outcome of MarkDebating. Duplicate is
// true on the idempotent re-mark path: the source was already
// debating, no status change was performed, and a single
// knowledge.debate_opened audit row was written with
// Metadata.duplicate=true and Before=After={status:"debating"}.
// On the normal path Duplicate is false and TWO audit rows are
// written: knowledge.status_changed (the generic status flip) and
// knowledge.debate_opened (the domain-specific event).
type MarkDebatingResult struct {
	ObjectID     uuid.UUID
	Status       string
	AuditEventID uuid.UUID
	Duplicate    bool
}

// MarkDebating transitions a proposed object to debating and emits
// a knowledge.debate_opened audit event. On the duplicate path
// (source already debating) it emits ONLY a knowledge.debate_opened
// audit row with Metadata.duplicate=true; the status is not changed
// and no status_changed companion is written.
//
// Returns ErrInvalidTransition when the source status is not
// "proposed" or "debating" (i.e., the object is validated,
// deprecated, or active). Returns ErrNotFound when the object does
// not exist for the given (workspace, objectID). Audit insert
// failures roll back the status update.
func (s *ObjectDebateService) MarkDebating(ctx context.Context, req MarkDebatingRequest) (MarkDebatingResult, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(req.WorkspaceID))
	if !isValidDebateTrigger(req.TriggeredBy, req.SuggestedBy) {
		return MarkDebatingResult{}, ErrInvalidTransition
	}
	const targetStatus = domain.KnowledgeObjectStatusDebating

	var result MarkDebatingResult
	err := s.uow.WithinObjectDebateTx(ctx, func(ctx context.Context, repos ObjectDebateRepositories) error {
		object, err := repos.Objects().FindByIDForUpdate(ctx, workspaceID, req.ObjectID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrNotFound
			}
			return err
		}

		// Duplicate path: source already debating. Per spec, the
		// status_changed companion is OMITTED on this branch
		// because the status did not change. We write exactly one
		// audit row — the domain-specific one — with
		// Metadata.duplicate=true and Before=After={status:debating}.
		if object.Status == targetStatus {
			metadata := buildDebateAuditMetadata(req, true)
			auditEventID := s.ids()
			event := domain.AuditEvent{
				ID:          auditEventID,
				WorkspaceID: workspaceID,
				ActorID:     strings.TrimSpace(req.ActorID),
				Action:      domain.AuditActionKnowledgeDebateOpened,
				TargetType:  domain.AuditTargetKnowledgeObject,
				TargetID:    req.ObjectID,
				Before:      domain.Metadata{"status": targetStatus},
				After:       domain.Metadata{"status": targetStatus},
				Reason:      strings.TrimSpace(req.Reason),
				RequestID:   req.RequestID,
				Metadata:    metadata,
				CreatedAt:   s.now().UTC(),
			}
			if err := repos.AuditEvents().Create(ctx, event); err != nil {
				return err
			}
			result = MarkDebatingResult{
				ObjectID:     req.ObjectID,
				Status:       targetStatus,
				AuditEventID: auditEventID,
				Duplicate:    true,
			}
			return nil
		}

		// Normal path: source must be proposed. Any other source
		// (validated, deprecated, active) is rejected with
		// ErrInvalidTransition.
		if object.Status != domain.KnowledgeObjectStatusProposed {
			return ErrInvalidTransition
		}

		if err := repos.Objects().UpdateStatus(ctx, workspaceID, req.ObjectID, targetStatus); err != nil {
			return err
		}

		// Two audit events share ActorID, RequestID, Before, After,
		// and Reason. The status_changed event is the generic
		// status flip; the debate_opened event carries the
		// domain-specific metadata (suggested_by on system
		// initiation, no metadata on human-explicit initiation).
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

		debateOpenedID := s.ids()
		debateOpened := domain.AuditEvent{
			ID:          debateOpenedID,
			WorkspaceID: workspaceID,
			ActorID:     strings.TrimSpace(req.ActorID),
			Action:      domain.AuditActionKnowledgeDebateOpened,
			TargetType:  domain.AuditTargetKnowledgeObject,
			TargetID:    req.ObjectID,
			Before:      domain.Metadata{"status": object.Status},
			After:       domain.Metadata{"status": targetStatus},
			Reason:      strings.TrimSpace(req.Reason),
			RequestID:   req.RequestID,
			Metadata:    buildDebateAuditMetadata(req, false),
			CreatedAt:   s.now().UTC(),
		}
		if err := repos.AuditEvents().Create(ctx, debateOpened); err != nil {
			return err
		}

		result = MarkDebatingResult{
			ObjectID:     req.ObjectID,
			Status:       targetStatus,
			AuditEventID: debateOpenedID,
			Duplicate:    false,
		}
		return nil
	})
	if err != nil {
		return MarkDebatingResult{}, err
	}
	return result, nil
}
