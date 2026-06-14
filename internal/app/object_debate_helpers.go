package app

import (
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
