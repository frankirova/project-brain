package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ----------------------------------------------------------------------------
// MarkDebating
// ----------------------------------------------------------------------------

// TestMarkDebatingHumanExplicitHappyPath covers the human-explicit
// initiation path: TriggeredBy="human", SuggestedBy="". The service
// must transition the row from proposed to debating and emit TWO
// audit events sharing ActorID/RequestID/Before/After/Reason: a
// knowledge.status_changed (generic) and a knowledge.debate_opened
// (domain-specific). Metadata.suggested_by MUST be absent on the
// debate_opened row.
func TestMarkDebatingHumanExplicitHappyPath(t *testing.T) {
	auditIDGen := newSequentialIDs(
		uuid.MustParse("00000000-0000-0000-0000-000000000101"), // status_changed
		uuid.MustParse("00000000-0000-0000-0000-000000000102"), // debate_opened
	)
	now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
	requestID := uuid.MustParse("00000000-0000-0000-0000-000000000301")

	uow := newFakeDebateUOW(domain.KnowledgeObject{
		ID:          objectID,
		WorkspaceID: "workspace-1",
		Status:      domain.KnowledgeObjectStatusProposed,
	})
	service := NewObjectDebateServiceWithDependencies(uow, auditIDGen, func() time.Time { return now })

	result, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
		WorkspaceID: " Workspace-1 ",
		ObjectID:    objectID,
		TriggeredBy: domain.DebateTriggerHuman,
		SuggestedBy: "",
		ActorID:     " reviewer-1 ",
		Reason:      " opened by human ",
		RequestID:   &requestID,
	})
	if err != nil {
		t.Fatalf("MarkDebating() returned error: %v", err)
	}
	if !uow.committed || uow.rolledBack {
		t.Fatalf("transaction committed=%v rolledBack=%v, want committed only", uow.committed, uow.rolledBack)
	}
	if result.ObjectID != objectID || result.Status != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("result identity/status = %+v, want %v/debating", result, objectID)
	}
	if result.Duplicate {
		t.Fatalf("result.Duplicate = true, want false on normal path")
	}
	if result.AuditEventID != uuid.MustParse("00000000-0000-0000-0000-000000000102") {
		t.Fatalf("result.AuditEventID = %v, want the debate_opened row", result.AuditEventID)
	}
	if uow.repos.object.updatedStatus != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("updated status = %q, want debating", uow.repos.object.updatedStatus)
	}
	if got := len(uow.repos.audit.created); got != 2 {
		t.Fatalf("audit count = %d, want 2 (status_changed + debate_opened)", got)
	}

	statusChanged := uow.repos.audit.created[0]
	if statusChanged.ID != uuid.MustParse("00000000-0000-0000-0000-000000000101") {
		t.Fatalf("status_changed.ID = %v, want first generated", statusChanged.ID)
	}
	if statusChanged.Action != domain.AuditActionKnowledgeStatusChanged ||
		statusChanged.TargetType != domain.AuditTargetKnowledgeObject ||
		statusChanged.TargetID != objectID {
		t.Fatalf("status_changed target/action = %+v", statusChanged)
	}
	if statusChanged.WorkspaceID != "workspace-1" || statusChanged.ActorID != "reviewer-1" {
		t.Fatalf("status_changed identity = %+v", statusChanged)
	}
	if statusChanged.Before["status"] != domain.KnowledgeObjectStatusProposed ||
		statusChanged.After["status"] != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("status_changed before/after = %+v/%+v", statusChanged.Before, statusChanged.After)
	}
	if statusChanged.Reason != "opened by human" ||
		statusChanged.RequestID == nil ||
		*statusChanged.RequestID != requestID ||
		!statusChanged.CreatedAt.Equal(now) {
		t.Fatalf("status_changed context = %+v", statusChanged)
	}

	debateOpened := uow.repos.audit.created[1]
	if debateOpened.Action != domain.AuditActionKnowledgeDebateOpened {
		t.Fatalf("debate_opened.Action = %q, want %q", debateOpened.Action, domain.AuditActionKnowledgeDebateOpened)
	}
	// Dual-event emission must share ActorID, RequestID, Before, After, Reason
	// with the status_changed companion (per the spec: "shares the same
	// RequestID, ActorID, and Reason").
	if debateOpened.ActorID != statusChanged.ActorID ||
		debateOpened.WorkspaceID != statusChanged.WorkspaceID ||
		debateOpened.Reason != statusChanged.Reason {
		t.Fatalf("debate_opened identity mismatch vs status_changed: %+v vs %+v", debateOpened, statusChanged)
	}
	if debateOpened.RequestID == nil || statusChanged.RequestID == nil ||
		*debateOpened.RequestID != *statusChanged.RequestID {
		t.Fatalf("debate_opened.RequestID = %v, want same as status_changed (%v)", debateOpened.RequestID, statusChanged.RequestID)
	}
	if debateOpened.Before["status"] != domain.KnowledgeObjectStatusProposed ||
		debateOpened.After["status"] != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("debate_opened before/after = %+v/%+v", debateOpened.Before, debateOpened.After)
	}
	// Human-explicit MUST omit Metadata.suggested_by.
	if _, present := debateOpened.Metadata["suggested_by"]; present {
		t.Fatalf("debate_opened.Metadata.suggested_by present on human-explicit path, want absent: %+v", debateOpened.Metadata)
	}
	if _, present := debateOpened.Metadata["duplicate"]; present {
		t.Fatalf("debate_opened.Metadata.duplicate present on normal path, want absent: %+v", debateOpened.Metadata)
	}
}

// TestMarkDebatingSystemSuggestedHappyPath covers the system-suggested
// initiation path: TriggeredBy="system", SuggestedBy is a
// well-known bot identifier. The service must emit the same two
// audit events as the human-explicit path, but the debate_opened
// row MUST carry Metadata.suggested_by equal to the supplied
// SuggestedBy value.
func TestMarkDebatingSystemSuggestedHappyPath(t *testing.T) {
	auditIDGen := newSequentialIDs(
		uuid.MustParse("00000000-0000-0000-0000-000000000101"),
		uuid.MustParse("00000000-0000-0000-0000-000000000102"),
	)
	now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
	requestID := uuid.MustParse("00000000-0000-0000-0000-000000000301")

	uow := newFakeDebateUOW(domain.KnowledgeObject{
		ID:          objectID,
		WorkspaceID: "workspace-1",
		Status:      domain.KnowledgeObjectStatusProposed,
	})
	service := NewObjectDebateServiceWithDependencies(uow, auditIDGen, func() time.Time { return now })

	result, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
		WorkspaceID: "workspace-1",
		ObjectID:    objectID,
		TriggeredBy: domain.DebateTriggerSystem,
		SuggestedBy: domain.DebateSuggestionContradictionDetector,
		ActorID:     "user:42",
		Reason:      "bot suggested contradiction",
		RequestID:   &requestID,
	})
	if err != nil {
		t.Fatalf("MarkDebating() returned error: %v", err)
	}
	if result.Duplicate {
		t.Fatalf("result.Duplicate = true, want false on normal path")
	}
	if got := len(uow.repos.audit.created); got != 2 {
		t.Fatalf("audit count = %d, want 2 (status_changed + debate_opened)", got)
	}
	debateOpened := uow.repos.audit.created[1]
	if debateOpened.Metadata["suggested_by"] != domain.DebateSuggestionContradictionDetector {
		t.Fatalf("debate_opened.Metadata.suggested_by = %v, want %q", debateOpened.Metadata["suggested_by"], domain.DebateSuggestionContradictionDetector)
	}
	if _, present := debateOpened.Metadata["duplicate"]; present {
		t.Fatalf("debate_opened.Metadata.duplicate present on normal path, want absent: %+v", debateOpened.Metadata)
	}
	// Status_changed event must remain unchanged: it has no suggested_by
	// in any path (per spec, only the domain-specific event carries it).
	if _, present := uow.repos.audit.created[0].Metadata["suggested_by"]; present {
		t.Fatalf("status_changed.Metadata.suggested_by present, want absent on status_changed event: %+v", uow.repos.audit.created[0].Metadata)
	}
}

// TestMarkDebatingIdempotentOnDebatingSource covers the
// idempotent re-mark path. When the source is already debating,
// the service must NOT change the status and MUST write exactly
// ONE audit row (the domain-specific debate_opened) with
// Metadata.duplicate=true and Before=After={status:"debating"}.
// The status_changed companion MUST be omitted because the
// status did not change.
func TestMarkDebatingIdempotentOnDebatingSource(t *testing.T) {
	auditID := uuid.MustParse("00000000-0000-0000-0000-000000000101")
	now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
	objectID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
	requestID := uuid.MustParse("00000000-0000-0000-0000-000000000301")

	uow := newFakeDebateUOW(domain.KnowledgeObject{
		ID:          objectID,
		WorkspaceID: "workspace-1",
		Status:      domain.KnowledgeObjectStatusDebating,
	})
	service := NewObjectDebateServiceWithDependencies(uow, func() uuid.UUID { return auditID }, func() time.Time { return now })

	result, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
		WorkspaceID: "workspace-1",
		ObjectID:    objectID,
		TriggeredBy: domain.DebateTriggerHuman,
		ActorID:     "user:42",
		Reason:      "reminded",
		RequestID:   &requestID,
	})
	if err != nil {
		t.Fatalf("MarkDebating() returned error: %v", err)
	}
	if !result.Duplicate {
		t.Fatalf("result.Duplicate = false, want true on duplicate path")
	}
	if result.Status != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("result.Status = %q, want debating", result.Status)
	}
	if result.AuditEventID != auditID {
		t.Fatalf("result.AuditEventID = %v, want %v", result.AuditEventID, auditID)
	}
	if uow.repos.object.updatedStatus != "" {
		t.Fatalf("updated status = %q, want empty (no status change on duplicate path)", uow.repos.object.updatedStatus)
	}
	if got := len(uow.repos.audit.created); got != 1 {
		t.Fatalf("audit count = %d, want 1 (debate_opened only, NO status_changed companion on duplicate path)", got)
	}
	event := uow.repos.audit.created[0]
	if event.Action != domain.AuditActionKnowledgeDebateOpened {
		t.Fatalf("event.Action = %q, want %q", event.Action, domain.AuditActionKnowledgeDebateOpened)
	}
	if event.Before["status"] != domain.KnowledgeObjectStatusDebating ||
		event.After["status"] != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("event before/after = %+v/%+v, want both debating", event.Before, event.After)
	}
	if event.Metadata["duplicate"] != true {
		t.Fatalf("event.Metadata.duplicate = %v, want true", event.Metadata["duplicate"])
	}
	if _, present := event.Metadata["suggested_by"]; present {
		t.Fatalf("event.Metadata.suggested_by present on duplicate path, want absent: %+v", event.Metadata)
	}
}

// TestMarkDebatingRejectsNonProposedSources covers the
// invalid-transition rejection on MarkDebating: source status must
// be "proposed" (normal path) or "debating" (duplicate path). Any
// other source (validated, deprecated, active) MUST return
// ErrInvalidTransition and roll back the UoW without writing
// anything. The check happens INSIDE the transaction (after
// FindByIDForUpdate locks the row) so the UoW does start; the
// observable contract is rollback-not-commit and no writes.
func TestMarkDebatingRejectsNonProposedSources(t *testing.T) {
	cases := []struct {
		name   string
		status string
	}{
		{name: "validated source", status: domain.KnowledgeObjectStatusValidated},
		{name: "deprecated source", status: domain.KnowledgeObjectStatusDeprecated},
		{name: "active source", status: domain.KnowledgeObjectStatusActive},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			uow := newFakeDebateUOW(domain.KnowledgeObject{Status: tt.status})
			service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

			_, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
				WorkspaceID: "workspace-1",
				ObjectID:    uuid.New(),
				TriggeredBy: domain.DebateTriggerHuman,
			})
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("MarkDebating() error = %v, want ErrInvalidTransition", err)
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

// TestMarkDebatingRejectsInvalidTrigger covers the dual-initiator
// contract: TriggeredBy must be "system" or "human", and the
// (TriggeredBy, SuggestedBy) pair must be well-formed. Unknown
// triggers, system-without-suggestion, and human-with-suggestion
// must all return ErrInvalidTransition before any DB write.
func TestMarkDebatingRejectsInvalidTrigger(t *testing.T) {
	cases := []struct {
		name      string
		triggered string
		suggested string
	}{
		{name: "unknown trigger", triggered: "bot", suggested: ""},
		{name: "empty trigger", triggered: "", suggested: ""},
		{name: "system without suggestion", triggered: domain.DebateTriggerSystem, suggested: ""},
		{name: "system with unknown suggestion", triggered: domain.DebateTriggerSystem, suggested: "bot:some-other-bot"},
		{name: "human with suggestion", triggered: domain.DebateTriggerHuman, suggested: domain.DebateSuggestionContradictionDetector},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			uow := newFakeDebateUOW(domain.KnowledgeObject{Status: domain.KnowledgeObjectStatusProposed})
			service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

			_, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
				WorkspaceID: "workspace-1",
				ObjectID:    uuid.New(),
				TriggeredBy: tt.triggered,
				SuggestedBy: tt.suggested,
			})
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("MarkDebating() error = %v, want ErrInvalidTransition", err)
			}
			if uow.started || uow.repos.object.updatedStatus != "" || len(uow.repos.audit.created) != 0 {
				t.Fatalf("started=%v updated=%q audits=%d, want no writes", uow.started, uow.repos.object.updatedStatus, len(uow.repos.audit.created))
			}
		})
	}
}

// TestMarkDebatingNotFound covers the missing-object path: the
// service must return ErrNotFound and not write any audit events
// when the (workspace, objectID) pair does not exist.
func TestMarkDebatingNotFound(t *testing.T) {
	uow := newFakeDebateUOW(domain.KnowledgeObject{})
	uow.repos.object.findErr = ErrNotFound
	service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
		WorkspaceID: "workspace-1",
		ObjectID:    uuid.New(),
		TriggeredBy: domain.DebateTriggerHuman,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkDebating() error = %v, want ErrNotFound", err)
	}
	if !uow.rolledBack || uow.committed {
		t.Fatalf("transaction committed=%v rolledBack=%v, want rollback only", uow.committed, uow.rolledBack)
	}
	if uow.repos.object.updatedStatus != "" || len(uow.repos.audit.created) != 0 {
		t.Fatalf("updated=%q audits=%d, want no writes", uow.repos.object.updatedStatus, len(uow.repos.audit.created))
	}
}

// TestMarkDebatingRollsBackWhenAuditFails covers the atomicity
// contract: a status update that succeeds but an audit insert
// that fails must roll back the status change. Mirrors the
// TestValidateObjectRollsBackWhenAuditFails pattern.
func TestMarkDebatingRollsBackWhenAuditFails(t *testing.T) {
	failure := errors.New("audit insert failed")
	uow := newFakeDebateUOW(domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "workspace-1",
		Status:      domain.KnowledgeObjectStatusProposed,
	})
	uow.repos.audit.err = failure
	service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
		WorkspaceID: "workspace-1",
		ObjectID:    uow.repos.object.object.ID,
		TriggeredBy: domain.DebateTriggerHuman,
	})
	if !errors.Is(err, failure) {
		t.Fatalf("MarkDebating() error = %v, want audit failure", err)
	}
	if !uow.rolledBack || uow.committed {
		t.Fatalf("transaction committed=%v rolledBack=%v, want rollback only", uow.committed, uow.rolledBack)
	}
	if uow.repos.object.updatedStatus != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("updatedStatus = %q, want attempted debating write before rollback signal", uow.repos.object.updatedStatus)
	}
}

// TestMarkDebatingDualInitiatorProducesSameEndState verifies the
// spec invariant: regardless of who initiated the suggestion, the
// post-transition object status is the same and the audit row
// count is the same. The only difference between the two paths is
// the Metadata.suggested_by key on the debate_opened row.
func TestMarkDebatingDualInitiatorProducesSameEndState(t *testing.T) {
	cases := []struct {
		name       string
		triggered  string
		suggested  string
		wantOnMeta map[string]any
		notOnMeta  []string
	}{
		{
			name:      "system suggested",
			triggered: domain.DebateTriggerSystem,
			suggested: domain.DebateSuggestionContradictionDetector,
			wantOnMeta: map[string]any{
				"suggested_by": domain.DebateSuggestionContradictionDetector,
			},
			notOnMeta: []string{"duplicate"},
		},
		{
			name:       "human explicit",
			triggered:  domain.DebateTriggerHuman,
			suggested:  "",
			wantOnMeta: map[string]any{},
			notOnMeta:  []string{"suggested_by", "duplicate"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			auditIDGen := newSequentialIDs(
				uuid.MustParse("00000000-0000-0000-0000-000000000101"),
				uuid.MustParse("00000000-0000-0000-0000-000000000102"),
			)
			now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
			objectID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
			uow := newFakeDebateUOW(domain.KnowledgeObject{
				ID:          objectID,
				WorkspaceID: "workspace-1",
				Status:      domain.KnowledgeObjectStatusProposed,
			})
			service := NewObjectDebateServiceWithDependencies(uow, auditIDGen, func() time.Time { return now })

			result, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
				WorkspaceID: "workspace-1",
				ObjectID:    objectID,
				TriggeredBy: tt.triggered,
				SuggestedBy: tt.suggested,
				ActorID:     "user:42",
			})
			if err != nil {
				t.Fatalf("MarkDebating() returned error: %v", err)
			}
			if result.Duplicate {
				t.Fatalf("result.Duplicate = true, want false on normal path")
			}
			if result.Status != domain.KnowledgeObjectStatusDebating {
				t.Fatalf("result.Status = %q, want debating", result.Status)
			}
			if uow.repos.object.updatedStatus != domain.KnowledgeObjectStatusDebating {
				t.Fatalf("updated status = %q, want debating", uow.repos.object.updatedStatus)
			}
			if got := len(uow.repos.audit.created); got != 2 {
				t.Fatalf("audit count = %d, want 2 (status_changed + debate_opened) regardless of initiator", got)
			}
			debateOpened := uow.repos.audit.created[1]
			for k, v := range tt.wantOnMeta {
				if debateOpened.Metadata[k] != v {
					t.Fatalf("debate_opened.Metadata[%q] = %v, want %v (full: %+v)", k, debateOpened.Metadata[k], v, debateOpened.Metadata)
				}
			}
			for _, k := range tt.notOnMeta {
				if _, present := debateOpened.Metadata[k]; present {
					t.Fatalf("debate_opened.Metadata[%q] present, want absent (full: %+v)", k, debateOpened.Metadata)
				}
			}
		})
	}
}

// ----------------------------------------------------------------------------
// ResolveDebate
// ----------------------------------------------------------------------------

// TestResolveDebateHappyPath covers the two terminal outcomes of
// ResolveDebate: debating→validated (positive) and
// debating→deprecated (negative). Both paths must emit two audit
// rows (status_changed + debate_resolved) sharing
// ActorID/RequestID/Before/After/Reason, and the status update
// must land.
func TestResolveDebateHappyPath(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{name: "resolve positively", target: domain.KnowledgeObjectStatusValidated},
		{name: "resolve negatively", target: domain.KnowledgeObjectStatusDeprecated},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			auditIDGen := newSequentialIDs(
				uuid.MustParse("00000000-0000-0000-0000-000000000101"),
				uuid.MustParse("00000000-0000-0000-0000-000000000102"),
			)
			now := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC)
			objectID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
			requestID := uuid.MustParse("00000000-0000-0000-0000-000000000301")

			uow := newFakeDebateUOW(domain.KnowledgeObject{
				ID:          objectID,
				WorkspaceID: "workspace-1",
				Status:      domain.KnowledgeObjectStatusDebating,
			})
			service := NewObjectDebateServiceWithDependencies(uow, auditIDGen, func() time.Time { return now })

			result, err := service.ResolveDebate(context.Background(), ResolveDebateRequest{
				WorkspaceID:  "workspace-1",
				ObjectID:     objectID,
				TargetStatus: tt.target,
				ActorID:      "reviewer-1",
				Reason:       "debate resolved",
				RequestID:    &requestID,
			})
			if err != nil {
				t.Fatalf("ResolveDebate() returned error: %v", err)
			}
			if !uow.committed || uow.rolledBack {
				t.Fatalf("transaction committed=%v rolledBack=%v, want committed only", uow.committed, uow.rolledBack)
			}
			if result.ObjectID != objectID || result.Status != tt.target {
				t.Fatalf("result = %+v, want object/%s", result, tt.target)
			}
			if uow.repos.object.updatedStatus != tt.target {
				t.Fatalf("updated status = %q, want %q", uow.repos.object.updatedStatus, tt.target)
			}
			if got := len(uow.repos.audit.created); got != 2 {
				t.Fatalf("audit count = %d, want 2 (status_changed + debate_resolved)", got)
			}
			statusChanged := uow.repos.audit.created[0]
			debateResolved := uow.repos.audit.created[1]
			if statusChanged.Action != domain.AuditActionKnowledgeStatusChanged {
				t.Fatalf("first audit.Action = %q, want %q", statusChanged.Action, domain.AuditActionKnowledgeStatusChanged)
			}
			if debateResolved.Action != domain.AuditActionKnowledgeDebateResolved {
				t.Fatalf("second audit.Action = %q, want %q", debateResolved.Action, domain.AuditActionKnowledgeDebateResolved)
			}
			if statusChanged.ActorID != debateResolved.ActorID ||
				statusChanged.WorkspaceID != debateResolved.WorkspaceID ||
				statusChanged.Reason != debateResolved.Reason {
				t.Fatalf("audit identity mismatch: %+v vs %+v", statusChanged, debateResolved)
			}
			if statusChanged.RequestID == nil || debateResolved.RequestID == nil ||
				*statusChanged.RequestID != *debateResolved.RequestID {
				t.Fatalf("audit RequestID mismatch: %v vs %v", statusChanged.RequestID, debateResolved.RequestID)
			}
			if statusChanged.Before["status"] != domain.KnowledgeObjectStatusDebating ||
				statusChanged.After["status"] != tt.target {
				t.Fatalf("status_changed before/after = %+v/%+v", statusChanged.Before, statusChanged.After)
			}
			if debateResolved.Before["status"] != domain.KnowledgeObjectStatusDebating ||
				debateResolved.After["status"] != tt.target {
				t.Fatalf("debate_resolved before/after = %+v/%+v", debateResolved.Before, debateResolved.After)
			}
			if result.AuditEventID != uuid.MustParse("00000000-0000-0000-0000-000000000102") {
				t.Fatalf("result.AuditEventID = %v, want the debate_resolved row", result.AuditEventID)
			}
		})
	}
}

// TestResolveDebateRejectsInvalidTargets covers the target-only
// guard: ResolveDebate accepts "validated" and "deprecated"
// targets only. Everything else (debating, proposed, active,
// empty) is rejected with ErrInvalidTransition before any DB
// write. Note: "debating" is rejected because it would be a no-op;
// "proposed" is rejected because the source must be debating.
func TestResolveDebateRejectsInvalidTargets(t *testing.T) {
	for _, target := range []string{
		domain.KnowledgeObjectStatusDebating,
		domain.KnowledgeObjectStatusProposed,
		domain.KnowledgeObjectStatusActive,
		"",
	} {
		t.Run("target "+target, func(t *testing.T) {
			uow := newFakeDebateUOW(domain.KnowledgeObject{Status: domain.KnowledgeObjectStatusDebating})
			service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

			_, err := service.ResolveDebate(context.Background(), ResolveDebateRequest{
				WorkspaceID:  "workspace-1",
				ObjectID:     uuid.New(),
				TargetStatus: target,
			})
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("ResolveDebate() error = %v, want ErrInvalidTransition", err)
			}
			if uow.started || uow.repos.object.updatedStatus != "" || len(uow.repos.audit.created) != 0 {
				t.Fatalf("started=%v updated=%q audits=%d, want no writes", uow.started, uow.repos.object.updatedStatus, len(uow.repos.audit.created))
			}
		})
	}
}

// TestResolveDebateRejectsNonDebatingSource covers the source-only
// guard: ResolveDebate accepts "debating" as source only. Any
// other source (proposed, validated, deprecated, active) is
// rejected with ErrInvalidTransition.
func TestResolveDebateRejectsNonDebatingSource(t *testing.T) {
	cases := []struct {
		name   string
		status string
	}{
		{name: "proposed source", status: domain.KnowledgeObjectStatusProposed},
		{name: "validated source", status: domain.KnowledgeObjectStatusValidated},
		{name: "deprecated source", status: domain.KnowledgeObjectStatusDeprecated},
		{name: "active source", status: domain.KnowledgeObjectStatusActive},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			uow := newFakeDebateUOW(domain.KnowledgeObject{Status: tt.status})
			service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

			_, err := service.ResolveDebate(context.Background(), ResolveDebateRequest{
				WorkspaceID:  "workspace-1",
				ObjectID:     uuid.New(),
				TargetStatus: domain.KnowledgeObjectStatusValidated,
			})
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("ResolveDebate() error = %v, want ErrInvalidTransition", err)
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

// TestResolveDebateNotFound covers the missing-object path: the
// service must return ErrNotFound and not write any audit events
// when the (workspace, objectID) pair does not exist.
func TestResolveDebateNotFound(t *testing.T) {
	uow := newFakeDebateUOW(domain.KnowledgeObject{Status: domain.KnowledgeObjectStatusDebating})
	uow.repos.object.findErr = ErrNotFound
	service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.ResolveDebate(context.Background(), ResolveDebateRequest{
		WorkspaceID:  "workspace-1",
		ObjectID:     uuid.New(),
		TargetStatus: domain.KnowledgeObjectStatusValidated,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveDebate() error = %v, want ErrNotFound", err)
	}
	if !uow.rolledBack || uow.committed {
		t.Fatalf("transaction committed=%v rolledBack=%v, want rollback only", uow.committed, uow.rolledBack)
	}
	if uow.repos.object.updatedStatus != "" || len(uow.repos.audit.created) != 0 {
		t.Fatalf("updated=%q audits=%d, want no writes", uow.repos.object.updatedStatus, len(uow.repos.audit.created))
	}
}

// TestResolveDebateRollsBackWhenAuditFails covers the atomicity
// contract: a status update that succeeds but an audit insert
// that fails must roll back the status change.
func TestResolveDebateRollsBackWhenAuditFails(t *testing.T) {
	failure := errors.New("audit insert failed")
	uow := newFakeDebateUOW(domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "workspace-1",
		Status:      domain.KnowledgeObjectStatusDebating,
	})
	uow.repos.audit.err = failure
	service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.ResolveDebate(context.Background(), ResolveDebateRequest{
		WorkspaceID:  "workspace-1",
		ObjectID:     uow.repos.object.object.ID,
		TargetStatus: domain.KnowledgeObjectStatusValidated,
	})
	if !errors.Is(err, failure) {
		t.Fatalf("ResolveDebate() error = %v, want audit failure", err)
	}
	if !uow.rolledBack || uow.committed {
		t.Fatalf("transaction committed=%v rolledBack=%v, want rollback only", uow.committed, uow.rolledBack)
	}
	if uow.repos.object.updatedStatus != domain.KnowledgeObjectStatusValidated {
		t.Fatalf("updatedStatus = %q, want attempted validated write before rollback signal", uow.repos.object.updatedStatus)
	}
}

// ----------------------------------------------------------------------------
// isAllowedDebateTarget / isAllowedResolveTarget
// ----------------------------------------------------------------------------

// TestIsAllowedDebateTarget pins the debate-target matrix.
// "debating" is accepted (MarkDebating), "validated" and
// "deprecated" are accepted (ResolveDebate), every other status is
// rejected. The guard is target-only; source enforcement is the
// service's responsibility.
func TestIsAllowedDebateTarget(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{status: domain.KnowledgeObjectStatusDebating, want: true},
		{status: domain.KnowledgeObjectStatusValidated, want: true},
		{status: domain.KnowledgeObjectStatusDeprecated, want: true},
		{status: domain.KnowledgeObjectStatusProposed, want: false},
		{status: domain.KnowledgeObjectStatusActive, want: false},
		{status: "", want: false},
		{status: "unknown", want: false},
	}
	for _, tt := range cases {
		t.Run("status="+tt.status, func(t *testing.T) {
			if got := isAllowedDebateTarget(tt.status); got != tt.want {
				t.Fatalf("isAllowedDebateTarget(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

// TestIsAllowedResolveTarget pins the resolve-target matrix.
// "validated" and "deprecated" are accepted; "debating" is
// rejected (no-op), "proposed"/"active"/"" are rejected (not in
// the matrix).
func TestIsAllowedResolveTarget(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{status: domain.KnowledgeObjectStatusValidated, want: true},
		{status: domain.KnowledgeObjectStatusDeprecated, want: true},
		{status: domain.KnowledgeObjectStatusDebating, want: false},
		{status: domain.KnowledgeObjectStatusProposed, want: false},
		{status: domain.KnowledgeObjectStatusActive, want: false},
		{status: "", want: false},
		{status: "unknown", want: false},
	}
	for _, tt := range cases {
		t.Run("status="+tt.status, func(t *testing.T) {
			if got := isAllowedResolveTarget(tt.status); got != tt.want {
				t.Fatalf("isAllowedResolveTarget(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// isValidDebateTrigger / isKnownDebateSuggestion
// ----------------------------------------------------------------------------

// TestIsValidDebateTrigger pins the dual-initiator guard:
// "system" requires a known suggestion; "human" requires empty
// suggestion; everything else is rejected.
func TestIsValidDebateTrigger(t *testing.T) {
	cases := []struct {
		name      string
		triggered string
		suggested string
		want      bool
	}{
		{name: "system with known suggestion", triggered: domain.DebateTriggerSystem, suggested: domain.DebateSuggestionContradictionDetector, want: true},
		{name: "system with empty suggestion", triggered: domain.DebateTriggerSystem, suggested: "", want: false},
		{name: "system with unknown suggestion", triggered: domain.DebateTriggerSystem, suggested: "bot:other", want: false},
		{name: "human with empty suggestion", triggered: domain.DebateTriggerHuman, suggested: "", want: true},
		{name: "human with non-empty suggestion", triggered: domain.DebateTriggerHuman, suggested: domain.DebateSuggestionContradictionDetector, want: false},
		{name: "empty trigger", triggered: "", suggested: "", want: false},
		{name: "unknown trigger", triggered: "bot", suggested: "", want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidDebateTrigger(tt.triggered, tt.suggested); got != tt.want {
				t.Fatalf("isValidDebateTrigger(%q, %q) = %v, want %v", tt.triggered, tt.suggested, got, tt.want)
			}
		})
	}
}

// TestIsKnownDebateSuggestion pins the closed-set of well-known
// bot identifiers. Today only the contradiction detector is
// whitelisted; new bots must be added here (and in the spec) before
// the service will accept them.
func TestIsKnownDebateSuggestion(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{id: domain.DebateSuggestionContradictionDetector, want: true},
		{id: "", want: false},
		{id: "bot:some-other-bot", want: false},
		{id: "contradiction-detector", want: false}, // missing required "bot:" prefix
	}
	for _, tt := range cases {
		t.Run("id="+tt.id, func(t *testing.T) {
			if got := isKnownDebateSuggestion(tt.id); got != tt.want {
				t.Fatalf("isKnownDebateSuggestion(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// buildDebateAuditMetadata
// ----------------------------------------------------------------------------

// TestBuildDebateAuditMetadata pins the metadata shape for the
// four possible combinations: (human | system) × (normal |
// duplicate). Human-explicit + normal yields an empty map;
// system-suggested + normal yields {"suggested_by": ...}; either
// trigger + duplicate yields {"duplicate": true} without
// suggested_by. The shape is the spec contract for downstream
// consumers.
func TestBuildDebateAuditMetadata(t *testing.T) {
	cases := []struct {
		name           string
		req            MarkDebatingRequest
		duplicate      bool
		wantKeys       map[string]any
		wantAbsentKeys []string
	}{
		{
			name: "human explicit normal",
			req: MarkDebatingRequest{
				TriggeredBy: domain.DebateTriggerHuman,
				SuggestedBy: "",
			},
			duplicate:      false,
			wantKeys:       map[string]any{},
			wantAbsentKeys: []string{"suggested_by", "duplicate"},
		},
		{
			name: "system suggested normal",
			req: MarkDebatingRequest{
				TriggeredBy: domain.DebateTriggerSystem,
				SuggestedBy: domain.DebateSuggestionContradictionDetector,
			},
			duplicate: false,
			wantKeys: map[string]any{
				"suggested_by": domain.DebateSuggestionContradictionDetector,
			},
			wantAbsentKeys: []string{"duplicate"},
		},
		{
			name: "human explicit duplicate",
			req: MarkDebatingRequest{
				TriggeredBy: domain.DebateTriggerHuman,
				SuggestedBy: "",
			},
			duplicate: true,
			wantKeys: map[string]any{
				"duplicate": true,
			},
			wantAbsentKeys: []string{"suggested_by"},
		},
		{
			name: "system suggested duplicate",
			req: MarkDebatingRequest{
				TriggeredBy: domain.DebateTriggerSystem,
				SuggestedBy: domain.DebateSuggestionContradictionDetector,
			},
			duplicate: true,
			wantKeys: map[string]any{
				"duplicate": true,
			},
			wantAbsentKeys: []string{"suggested_by"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDebateAuditMetadata(tt.req, tt.duplicate)
			if got == nil {
				t.Fatalf("buildDebateAuditMetadata returned nil, want non-nil map")
			}
			for k, v := range tt.wantKeys {
				if got[k] != v {
					t.Fatalf("metadata[%q] = %v, want %v (full: %+v)", k, got[k], v, got)
				}
			}
			for _, k := range tt.wantAbsentKeys {
				if _, present := got[k]; present {
					t.Fatalf("metadata[%q] present, want absent (full: %+v)", k, got)
				}
			}
		})
	}
}

// ----------------------------------------------------------------------------
// Workspace normalization
// ----------------------------------------------------------------------------

// TestMarkDebatingNormalizesWorkspaceID verifies that the
// service lowercases and trims the workspace ID before querying
// the repository, mirroring the validation service behavior.
// The audit WorkspaceID is the observable downstream effect —
// the lookup workspace is captured on the fake object repo so we
// can also assert the call site was normalized.
func TestMarkDebatingNormalizesWorkspaceID(t *testing.T) {
	uow := newFakeDebateUOW(domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "workspace-1",
		Status:      domain.KnowledgeObjectStatusProposed,
	})
	service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.MarkDebating(context.Background(), MarkDebatingRequest{
		WorkspaceID: "  Workspace-1  ",
		ObjectID:    uow.repos.object.object.ID,
		TriggeredBy: domain.DebateTriggerHuman,
		ActorID:     "user:1",
	})
	if err != nil {
		t.Fatalf("MarkDebating() returned error: %v", err)
	}
	if uow.repos.object.lastWorkspaceID != "workspace-1" {
		t.Fatalf("repo.FindByIDForUpdate saw workspace %q, want normalized %q", uow.repos.object.lastWorkspaceID, "workspace-1")
	}
	if uow.repos.audit.created[0].WorkspaceID != "workspace-1" {
		t.Fatalf("audit WorkspaceID = %q, want normalized %q", uow.repos.audit.created[0].WorkspaceID, "workspace-1")
	}
	if uow.repos.object.updatedWorkspaceID != "workspace-1" {
		t.Fatalf("repo.UpdateStatus saw workspace %q, want normalized %q", uow.repos.object.updatedWorkspaceID, "workspace-1")
	}
}

// TestResolveDebateNormalizesWorkspaceID mirrors the
// MarkDebating normalization test for the ResolveDebate path.
func TestResolveDebateNormalizesWorkspaceID(t *testing.T) {
	uow := newFakeDebateUOW(domain.KnowledgeObject{
		ID:          uuid.New(),
		WorkspaceID: "workspace-1",
		Status:      domain.KnowledgeObjectStatusDebating,
	})
	service := NewObjectDebateServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.ResolveDebate(context.Background(), ResolveDebateRequest{
		WorkspaceID:  "  Workspace-1  ",
		ObjectID:     uow.repos.object.object.ID,
		TargetStatus: domain.KnowledgeObjectStatusValidated,
		ActorID:      "user:1",
	})
	if err != nil {
		t.Fatalf("ResolveDebate() returned error: %v", err)
	}
	if uow.repos.object.lastWorkspaceID != "workspace-1" {
		t.Fatalf("repo.FindByIDForUpdate saw workspace %q, want normalized %q", uow.repos.object.lastWorkspaceID, "workspace-1")
	}
	if uow.repos.audit.created[0].WorkspaceID != "workspace-1" {
		t.Fatalf("audit WorkspaceID = %q, want normalized %q", uow.repos.audit.created[0].WorkspaceID, "workspace-1")
	}
	if uow.repos.object.updatedWorkspaceID != "workspace-1" {
		t.Fatalf("repo.UpdateStatus saw workspace %q, want normalized %q", uow.repos.object.updatedWorkspaceID, "workspace-1")
	}
}

// ----------------------------------------------------------------------------
// fakes (mirror of fakeValidationUOW)
// ----------------------------------------------------------------------------

type fakeDebateUOW struct {
	started    bool
	committed  bool
	rolledBack bool
	repos      *fakeDebateRepos
}

func newFakeDebateUOW(object domain.KnowledgeObject) *fakeDebateUOW {
	return &fakeDebateUOW{repos: &fakeDebateRepos{
		object: &fakeDebateObjectRepo{object: object},
		audit:  &fakeDebateAuditRepo{},
	}}
}

func (u *fakeDebateUOW) WithinObjectDebateTx(ctx context.Context, fn func(context.Context, ObjectDebateRepositories) error) error {
	u.started = true
	if err := fn(ctx, u.repos); err != nil {
		u.rolledBack = true
		return err
	}
	u.committed = true
	return nil
}

type fakeDebateRepos struct {
	object *fakeDebateObjectRepo
	audit  *fakeDebateAuditRepo
}

func (r *fakeDebateRepos) Objects() ObjectDebateObjectRepository { return r.object }
func (r *fakeDebateRepos) AuditEvents() AuditEventRepository     { return r.audit }

type fakeDebateObjectRepo struct {
	object             domain.KnowledgeObject
	findErr            error
	updatedStatus      string
	lastWorkspaceID    string
	updatedWorkspaceID string
}

func (r *fakeDebateObjectRepo) FindByIDForUpdate(_ context.Context, workspaceID string, _ uuid.UUID) (domain.KnowledgeObject, error) {
	r.lastWorkspaceID = workspaceID
	if r.findErr != nil {
		return domain.KnowledgeObject{}, r.findErr
	}
	return r.object, nil
}

func (r *fakeDebateObjectRepo) UpdateStatus(_ context.Context, workspaceID string, _ uuid.UUID, status string) error {
	r.updatedWorkspaceID = workspaceID
	r.updatedStatus = status
	return nil
}

type fakeDebateAuditRepo struct {
	err     error
	created []domain.AuditEvent
}

func (r *fakeDebateAuditRepo) Create(_ context.Context, event domain.AuditEvent) error {
	if r.err != nil {
		return r.err
	}
	r.created = append(r.created, event)
	return nil
}

// newSequentialIDs returns an IDGenerator that yields the supplied
// UUIDs in order, one per call. After the last supplied ID is
// consumed, subsequent calls panic. This makes it impossible for
// the test to silently drift past the expected audit-row count
// without a panic surfacing the bug.
func newSequentialIDs(ids ...uuid.UUID) IDGenerator {
	i := 0
	return func() uuid.UUID {
		if i >= len(ids) {
			panic("newSequentialIDs exhausted")
		}
		id := ids[i]
		i++
		return id
	}
}
