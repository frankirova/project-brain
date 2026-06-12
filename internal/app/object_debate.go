package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ObjectDebateService owns the proposed→debating and
// debating→{validated,deprecated} lifecycle transitions AND the
// read-only human backlog query. It is the sibling of
// ValidateObjectService and shares its atomicity contract: status
// update and audit event(s) commit in a single Postgres
// transaction; audit failure rolls back the status change.
//
// The service is channel-agnostic. Transport layers (HTTP, Telegram,
// future web) translate UI events into MarkDebating / ResolveDebate
// / ListHumanBacklog calls; no transport logic lives here.
//
// PR 2 (write path) added UoW + ID + Clock. PR 3 (read path) added
// the BacklogQuery port so the read surface can be exercised with
// a fake in unit tests.
type ObjectDebateService struct {
	uow   ObjectDebateUnitOfWork
	query BacklogQuery
	ids   IDGenerator
	now   Clock
}

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

// NewObjectDebateService is the production constructor. It injects
// the real UUID generator and time clock, and accepts the
// BacklogQuery port for the read path.
func NewObjectDebateService(uow ObjectDebateUnitOfWork, query BacklogQuery) *ObjectDebateService {
	return NewObjectDebateServiceWithDependencies(uow, query, uuid.New, time.Now)
}

// NewObjectDebateServiceWithDependencies lets tests inject a
// deterministic ID generator and clock. Mirrors the
// NewValidateObjectServiceWithDependencies pattern. The query
// parameter is also injected so unit tests can drive the read path
// with a fake; the production wiring in main.go passes the real
// postgres-backed newBacklogQuery.
func NewObjectDebateServiceWithDependencies(uow ObjectDebateUnitOfWork, query BacklogQuery, ids IDGenerator, now Clock) *ObjectDebateService {
	return &ObjectDebateService{uow: uow, query: query, ids: ids, now: now}
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

// isAllowedDebateTarget is the target-only guard for the debate
// service. It mirrors isAllowedValidationTarget (sibling, not
// widening) and accepts:
//
//   - "debating"    — MarkDebating
//   - "validated"   — ResolveDebate (positive)
//   - "deprecated"  — ResolveDebate (negative)
//
// Source-status enforcement (proposed for Mark, debating for
// Resolve) is the service's responsibility, not the guard's. The
// guard exists to reject syntactically invalid targets BEFORE the
// transaction starts, matching the validate_object pattern.
func isAllowedDebateTarget(status string) bool {
	switch status {
	case domain.KnowledgeObjectStatusDebating,
		domain.KnowledgeObjectStatusValidated,
		domain.KnowledgeObjectStatusDeprecated:
		return true
	default:
		return false
	}
}

// isAllowedResolveTarget is the ResolveDebate target guard. It is
// a strict subset of isAllowedDebateTarget: only the terminal
// resolution outcomes are accepted, never "debating" (which would
// be a no-op) or "proposed"/"active" (which are not in the
// debate matrix).
func isAllowedResolveTarget(status string) bool {
	switch status {
	case domain.KnowledgeObjectStatusValidated,
		domain.KnowledgeObjectStatusDeprecated:
		return true
	default:
		return false
	}
}

// isValidDebateTrigger enforces the dual-initiator contract for
// MarkDebating:
//
//   - "system" requires SuggestedBy to be set to a recognized bot
//     identifier; SuggestedBy is rendered into Metadata.suggested_by
//     on the debate_opened audit row.
//   - "human" requires SuggestedBy to be empty; Metadata.suggested_by
//     is omitted from the debate_opened audit row.
//   - Anything else (unknown trigger, system-without-suggestion,
//     human-with-suggestion) is rejected with ErrInvalidTransition.
//
// The SuggestionContradictionDetector is the only well-known
// identifier today; the set is closed and the value is whitelisted
// here. New bots must be added to this allowlist before the
// service will accept their SuggestionID.
func isValidDebateTrigger(triggeredBy, suggestedBy string) bool {
	switch triggeredBy {
	case domain.DebateTriggerSystem:
		return suggestedBy != "" && isKnownDebateSuggestion(suggestedBy)
	case domain.DebateTriggerHuman:
		return suggestedBy == ""
	default:
		return false
	}
}

// isKnownDebateSuggestion returns true if id is a member of the
// closed set of well-known bot identifiers that may populate
// Metadata.suggested_by. The set is intentionally small; new bots
// must be added here (and in the spec) before they can be used.
func isKnownDebateSuggestion(id string) bool {
	switch id {
	case domain.DebateSuggestionContradictionDetector:
		return true
	default:
		return false
	}
}

// buildDebateAuditMetadata assembles the Metadata map for a
// knowledge.debate_opened audit event.
//
// On the normal path (duplicate=false) the function returns:
//
//   - {"suggested_by": "<bot-id>"} when TriggeredBy="system" (the
//     value is rendered exactly as supplied; the caller has already
//     validated it via isValidDebateTrigger).
//   - {} (empty map, NOT nil) when TriggeredBy="human". An empty
//     map serializes to the JSON object {} and keeps the
//     "Metadata absent" semantics the spec requires: the field is
//     "omitted" in the sense that the JSON object is empty, so
//     downstream consumers can rely on `suggested_by not present`
//     to mean human-explicit.
//
// On the duplicate path (duplicate=true) the function returns
// {"duplicate": true} regardless of TriggeredBy. The system/human
// distinction is not preserved on a no-op re-mark because the
// status did not change; downstream consumers can use the duplicate
// flag to skip suggested_by correlation in that case.
//
// The function is total: any combination of inputs produces a
// non-nil map. The service writes the result directly into
// AuditEvent.Metadata; the postgres layer marshals nil-or-empty
// maps to '{}' (see marshalMetadata in repositories.go), so the
// distinction between nil and {} does not matter at the storage
// layer, but we keep the distinction at the API layer to honor
// the spec's "absent" language.
func buildDebateAuditMetadata(req MarkDebatingRequest, duplicate bool) domain.Metadata {
	metadata := domain.Metadata{}
	if duplicate {
		metadata["duplicate"] = true
		return metadata
	}
	if req.TriggeredBy == domain.DebateTriggerSystem {
		metadata["suggested_by"] = req.SuggestedBy
	}
	return metadata
}

// ListHumanBacklog returns a workspace-scoped, cursor-paginated
// worklist of knowledge objects needing human attention. The set
// is {proposed, debating, recently-deprecated-within-14d}. Each
// row carries the derived is_stale / stale_for_days markers so
// the UI can flag long-running debates.
//
// The service is a thin orchestration layer over the BacklogQuery
// port: it normalizes the workspace ID, clamps the page size,
// decodes the cursor, and post-processes the page to emit
// NextCursor exactly when more rows exist. The SQL projection
// (and the partial index that backs it) live in the postgres
// implementation.
//
// NextCursor contract: emitted iff the underlying query returned
// at least one more row than the page size (the standard
// "fetch N+1, trim to N, encode the Nth" keyset idiom). On the
// last page NextCursor is the empty string and the caller MUST
// stop paginating.
//
// Error contract: ErrInvalidCursor is returned (and no database
// read is issued) when the supplied cursor does not decode. Other
// errors are returned as-is from the BacklogQuery implementation.
func (s *ObjectDebateService) ListHumanBacklog(ctx context.Context, filter BacklogFilter) (BacklogPage, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(filter.WorkspaceID))
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = BacklogDefaultPageSize
	}
	if pageSize > BacklogMaxPageSize {
		pageSize = BacklogMaxPageSize
	}

	cursor := strings.TrimSpace(filter.Cursor)
	if cursor != "" {
		// Decode eagerly so a malformed cursor never reaches the
		// SQL planner. DecodeBacklogCursor returns ErrInvalidCursor
		// on every malformed-input branch (non-base64, base64-non-
		// JSON, missing keys, zero UUID) — the codec contract
		// inherited from PR 1.
		if _, _, err := DecodeBacklogCursor(cursor); err != nil {
			return BacklogPage{}, ErrInvalidCursor
		}
	}

	page, err := s.query.List(ctx, BacklogFilter{
		WorkspaceID: workspaceID,
		PageSize:    pageSize,
		Cursor:      cursor,
	})
	if err != nil {
		return BacklogPage{}, err
	}

	// "More rows available" sentinel: if the implementation
	// returned exactly pageSize+1 rows, we have a next page.
	// The implementation in postgres fetches LIMIT pageSize+1 and
	// returns the trimmed slice; a fake in tests is expected to
	// mirror the same shape.
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = EncodeBacklogCursor(last.UpdatedAt, last.ID)
	} else {
		// Ensure NextCursor is empty on the last page even if a
		// future implementation forgets to clear it. The postgres
		// implementation already returns an empty string, but the
		// guarantee belongs to the service, not the implementation.
		page.NextCursor = ""
	}
	return page, nil
}
