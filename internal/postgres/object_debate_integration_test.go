package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ----------------------------------------------------------------------------
// Write-path integration tests for ObjectDebateService.
//
// These tests exercise the live Postgres on port 5433 and are gated by
// the PROJECT_BRAIN_TEST_DATABASE_DSN env var (see openIntegrationDB).
// Migrations 0001..0012 (including 0011 raw_inputs and 0012
// idx_knowledge_objects_debating) are applied automatically by the
// shared openIntegrationDB helper, so no manual schema setup is
// required.
// ----------------------------------------------------------------------------

// TestPostgresMarkDebatingPersistsStatusAndAudit pins the round-trip
// for both TriggeredBy variants. The status flip is read back from
// the live database, the audit count is asserted (status_changed +
// debate_opened for the normal path), and the debate_opened
// Metadata is checked for the right suggested_by: present and equal
// to the supplied SuggestedBy on the system-suggested path, absent
// on the human-explicit path.
func TestPostgresMarkDebatingPersistsStatusAndAudit(t *testing.T) {
	cases := []struct {
		name        string
		triggeredBy string
		suggestedBy string
	}{
		{name: "human explicit", triggeredBy: domain.DebateTriggerHuman, suggestedBy: ""},
		{name: "system suggested", triggeredBy: domain.DebateTriggerSystem, suggestedBy: domain.DebateSuggestionContradictionDetector},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			db := openIntegrationDB(t)
			workspaceID := "workspace-" + uuid.NewString()
			t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
			objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
			svc := app.NewObjectDebateService(db, nil)

			result, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
				WorkspaceID: workspaceID,
				ObjectID:    objectID,
				TriggeredBy: tt.triggeredBy,
				SuggestedBy: tt.suggestedBy,
				ActorID:     "user-1",
				Reason:      "open debate",
			})
			if err != nil {
				t.Fatalf("MarkDebating: %v", err)
			}
			if result.Duplicate {
				t.Fatalf("Duplicate = true, want false on first call")
			}
			assertObjectStatus(t, db, workspaceID, objectID, domain.KnowledgeObjectStatusDebating)

			// Two audit events on the normal path: status_changed + debate_opened.
			assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeStatusChanged, 1)
			assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeDebateOpened, 1)

			metadata := fetchDebateOpenedMetadata(t, db, workspaceID, objectID)
			if tt.suggestedBy == "" {
				if _, present := metadata["suggested_by"]; present {
					t.Fatalf("human-explicit: Metadata.suggested_by present (%v), want absent", metadata["suggested_by"])
				}
			} else {
				if metadata["suggested_by"] != tt.suggestedBy {
					t.Fatalf("system-suggested: Metadata.suggested_by = %v, want %q", metadata["suggested_by"], tt.suggestedBy)
				}
			}
			if _, present := metadata["duplicate"]; present {
				t.Fatalf("Metadata.duplicate present on normal path, want absent: %+v", metadata)
			}
		})
	}
}

// TestPostgresMarkDebatingIdempotentReMark covers the duplicate
// path: a second MarkDebating on an already-debating object must
// return Duplicate=true, leave the status at debating, and write
// exactly ONE knowledge.debate_opened audit row with
// Metadata.duplicate=true and Before=After={status:"debating"}. The
// status_changed companion MUST NOT be written because the status
// did not change.
func TestPostgresMarkDebatingIdempotentReMark(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
	objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
	svc := app.NewObjectDebateService(db, nil)

	// First call: proposed -> debating (normal path, 2 audit events).
	if _, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
		WorkspaceID: workspaceID, ObjectID: objectID,
		TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
		ActorID: "user-1", Reason: "first",
	}); err != nil {
		t.Fatalf("first MarkDebating: %v", err)
	}
	assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeStatusChanged, 1)
	assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeDebateOpened, 1)

	// Second call: source is already debating (duplicate path, +1
	// event with Metadata.duplicate=true, no status_changed companion).
	result, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
		WorkspaceID: workspaceID, ObjectID: objectID,
		TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
		ActorID: "user-1", Reason: "second",
	})
	if err != nil {
		t.Fatalf("second MarkDebating: %v", err)
	}
	if !result.Duplicate {
		t.Fatalf("Duplicate = false, want true on re-mark of debating row")
	}
	assertObjectStatus(t, db, workspaceID, objectID, domain.KnowledgeObjectStatusDebating)
	assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeStatusChanged, 1)
	assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeDebateOpened, 2)

	// The most recent debate_opened row must carry
	// Metadata.duplicate=true and Before=After={status:"debating"}.
	row := db.pool.QueryRow(context.Background(), `
SELECT metadata, before, after
FROM audit_events
WHERE workspace_id = $1 AND target_id = $2 AND action = $3
ORDER BY created_at DESC LIMIT 1`,
		workspaceID, objectID, domain.AuditActionKnowledgeDebateOpened)
	var metadata domain.Metadata
	var before, after domain.Metadata
	if err := row.Scan(&metadata, &before, &after); err != nil {
		t.Fatalf("fetch latest debate_opened: %v", err)
	}
	if metadata["duplicate"] != true {
		t.Fatalf("latest debate_opened.Metadata.duplicate = %v, want true", metadata["duplicate"])
	}
	if before["status"] != domain.KnowledgeObjectStatusDebating || after["status"] != domain.KnowledgeObjectStatusDebating {
		t.Fatalf("latest debate_opened before/after = %+v/%+v, want debating/debating", before, after)
	}
}

// TestPostgresResolveDebatePersistsAndEmitsDebateResolved covers
// both terminal outcomes of ResolveDebate: target=validated and
// target=deprecated. Each must transition the row out of debating
// and emit two audit events (status_changed + debate_resolved) in a
// single transaction.
func TestPostgresResolveDebatePersistsAndEmitsDebateResolved(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{name: "to validated", target: domain.KnowledgeObjectStatusValidated},
		{name: "to deprecated", target: domain.KnowledgeObjectStatusDeprecated},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			db := openIntegrationDB(t)
			workspaceID := "workspace-" + uuid.NewString()
			t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
			objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
			svc := app.NewObjectDebateService(db, nil)

			// Open the debate first.
			if _, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
				WorkspaceID: workspaceID, ObjectID: objectID,
				TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
				ActorID: "user-1", Reason: "open",
			}); err != nil {
				t.Fatalf("MarkDebating: %v", err)
			}

			// Resolve it.
			if _, err := svc.ResolveDebate(context.Background(), app.ResolveDebateRequest{
				WorkspaceID: workspaceID, ObjectID: objectID,
				TargetStatus: tt.target, ActorID: "user-1", Reason: "resolve",
			}); err != nil {
				t.Fatalf("ResolveDebate: %v", err)
			}
			assertObjectStatus(t, db, workspaceID, objectID, tt.target)

			// Two new audit events: status_changed (debating->target)
			// and debate_resolved. The pre-existing status_changed +
			// debate_opened from MarkDebating stay on the timeline.
			var statusChangedCount, debateResolvedCount int
			if err := db.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND target_id = $2 AND action = $3`,
				workspaceID, objectID, domain.AuditActionKnowledgeStatusChanged).Scan(&statusChangedCount); err != nil {
				t.Fatalf("count status_changed: %v", err)
			}
			if statusChangedCount != 2 {
				t.Fatalf("status_changed count = %d, want 2 (one from Mark, one from Resolve)", statusChangedCount)
			}
			if err := db.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND target_id = $2 AND action = $3`,
				workspaceID, objectID, domain.AuditActionKnowledgeDebateResolved).Scan(&debateResolvedCount); err != nil {
				t.Fatalf("count debate_resolved: %v", err)
			}
			if debateResolvedCount != 1 {
				t.Fatalf("debate_resolved count = %d, want 1", debateResolvedCount)
			}

			// debate_resolved Before/After must be debating->target.
			var before, after domain.Metadata
			if err := db.pool.QueryRow(context.Background(),
				`SELECT before, after FROM audit_events WHERE workspace_id = $1 AND target_id = $2 AND action = $3`,
				workspaceID, objectID, domain.AuditActionKnowledgeDebateResolved).Scan(&before, &after); err != nil {
				t.Fatalf("fetch debate_resolved: %v", err)
			}
			if before["status"] != domain.KnowledgeObjectStatusDebating || after["status"] != tt.target {
				t.Fatalf("debate_resolved before/after = %+v/%+v, want debating/%s", before, after, tt.target)
			}
		})
	}
}

// TestPostgresMarkDebatingRollsBackWhenAuditFails exercises the
// WithinObjectDebateTx atomicity contract: a failing audit insert
// must roll back the status update, so the object stays at
// "proposed" and no audit row is visible.
func TestPostgresMarkDebatingRollsBackWhenAuditFails(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
	objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
	failure := errors.New("forced audit insert failure")
	svc := app.NewObjectDebateService(&failingAuditDebateUOW{db: db, err: failure}, nil)

	_, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
		WorkspaceID: workspaceID, ObjectID: objectID,
		TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
		ActorID: "user-1", Reason: "should fail",
	})
	if !errors.Is(err, failure) {
		t.Fatalf("MarkDebating err = %v, want audit failure", err)
	}
	assertObjectStatus(t, db, workspaceID, objectID, domain.KnowledgeObjectStatusProposed)
	assertAuditCount(t, db, workspaceID, 0)
}

// TestPostgresMarkDebatingSerializesConcurrentCalls verifies the
// FOR UPDATE lock from FindByIDForUpdate: two concurrent
// MarkDebating calls on the same (proposed) object must serialize.
// The first transitions the row and the second sees the now-debating
// status, taking the duplicate path. Both succeed; the status is
// debating; exactly 2 debate_opened rows and 1 status_changed row
// are persisted.
func TestPostgresMarkDebatingSerializesConcurrentCalls(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
	objectID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
	svc := app.NewObjectDebateService(db, nil)

	type outcome struct {
		duplicate bool
		err       error
	}
	results := make([]outcome, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
				WorkspaceID: workspaceID, ObjectID: objectID,
				TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
				ActorID: "user-1", Reason: "concurrent",
			})
			results[i] = outcome{duplicate: res.Duplicate, err: err}
		}()
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("goroutine %d: %v", i, r.err)
		}
	}
	// Exactly one caller must take the normal path, exactly one the
	// duplicate path. If FOR UPDATE didn't serialize, both would see
	// the row as proposed and both would attempt the transition.
	duplicates := 0
	for _, r := range results {
		if r.duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicate count = %d, want 1 (FOR UPDATE must serialize the second call onto the duplicate path); results = %+v", duplicates, results)
	}
	assertObjectStatus(t, db, workspaceID, objectID, domain.KnowledgeObjectStatusDebating)
	assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeStatusChanged, 1)
	assertDebateAuditCount(t, db, workspaceID, objectID, domain.AuditActionKnowledgeDebateOpened, 2)
}

// TestPostgresDebateIsWorkspaceScoped confirms the debate lifecycle
// is isolated by workspace_id. A MarkDebating call on workspace B
// must not affect a same-UUID row owned by workspace A.
//
// Workspace IDs are seeded lowercase to match the service's
// strings.ToLower(strings.TrimSpace(...)) normalization. The
// cross-workspace call below still exercises the rejection path:
// FindByIDForUpdate looks up (workspace_id=B, id=A_id) and the row
// is in workspace A, so the lookup misses and the service returns
// ErrNotFound.
func TestPostgresDebateIsWorkspaceScoped(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceA := "workspace-a-" + uuid.NewString()
	workspaceB := "workspace-b-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceA) })
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceB) })

	// Two objects in two workspaces sharing the same object ID is
	// impossible (the primary key is just the UUID), so use two
	// distinct object IDs and check that a transition in A leaves B
	// untouched.
	idA := seedKnowledgeObject(t, db, workspaceA, domain.KnowledgeObjectStatusProposed)
	idB := seedKnowledgeObject(t, db, workspaceB, domain.KnowledgeObjectStatusProposed)
	svc := app.NewObjectDebateService(db, nil)

	if _, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
		WorkspaceID: workspaceA, ObjectID: idA,
		TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
		ActorID: "user-1", Reason: "open A",
	}); err != nil {
		t.Fatalf("MarkDebating A: %v", err)
	}
	assertObjectStatus(t, db, workspaceA, idA, domain.KnowledgeObjectStatusDebating)
	assertObjectStatus(t, db, workspaceB, idB, domain.KnowledgeObjectStatusProposed)

	// A wrong-workspace call must return ErrNotFound (the service
	// scopes by (workspace_id, id)).
	_, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
		WorkspaceID: workspaceB, ObjectID: idA, // mismatched
		TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
		ActorID: "user-1", Reason: "should miss",
	})
	if !errors.Is(err, app.ErrNotFound) {
		t.Fatalf("cross-workspace MarkDebating err = %v, want ErrNotFound", err)
	}
	assertObjectStatus(t, db, workspaceA, idA, domain.KnowledgeObjectStatusDebating)
	assertObjectStatus(t, db, workspaceB, idB, domain.KnowledgeObjectStatusProposed)

	// Audit counts are strictly per-workspace.
	var auditsA, auditsB int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE workspace_id = $1`, workspaceA).Scan(&auditsA); err != nil {
		t.Fatalf("count audits A: %v", err)
	}
	if err := db.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE workspace_id = $1`, workspaceB).Scan(&auditsB); err != nil {
		t.Fatalf("count audits B: %v", err)
	}
	if auditsA != 2 {
		t.Fatalf("auditsA = %d, want 2 (status_changed + debate_opened)", auditsA)
	}
	if auditsB != 0 {
		t.Fatalf("auditsB = %d, want 0 (workspace B untouched)", auditsB)
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// assertDebateAuditCount asserts that the (workspace, object, action)
// audit triple has the expected row count. Used by all write-path
// tests in this file.
func assertDebateAuditCount(t *testing.T, db *DB, workspaceID string, objectID uuid.UUID, action string, want int) {
	t.Helper()
	var got int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND target_id = $2 AND action = $3`,
		workspaceID, objectID, action).Scan(&got); err != nil {
		t.Fatalf("count %s audits: %v", action, err)
	}
	if got != want {
		t.Fatalf("audit count for %s = %d, want %d", action, got, want)
	}
}

// fetchDebateOpenedMetadata returns the Metadata of the (single)
// debate_opened audit row tied to (workspace, object). The
// pre-condition (exactly one row) is asserted so a test that
// accidentally wrote two rows fails loudly with a useful message.
func fetchDebateOpenedMetadata(t *testing.T, db *DB, workspaceID string, objectID uuid.UUID) domain.Metadata {
	t.Helper()
	var metadata domain.Metadata
	if err := db.pool.QueryRow(context.Background(),
		`SELECT metadata FROM audit_events WHERE workspace_id = $1 AND target_id = $2 AND action = $3`,
		workspaceID, objectID, domain.AuditActionKnowledgeDebateOpened).Scan(&metadata); err != nil {
		t.Fatalf("fetch debate_opened metadata: %v", err)
	}
	return metadata
}

// failingAuditDebateUOW mirrors failingAuditValidationUOW from
// object_validation_integration_test.go but implements
// ObjectDebateUnitOfWork. It is used by the atomicity test to
// force a rollback without affecting the shared
// failingAuditValidationUOW (a future caller of this file from a
// test that also exercises the validation path could otherwise
// see cross-test interference).
type failingAuditDebateUOW struct {
	db  *DB
	err error
}

func (u *failingAuditDebateUOW) WithinObjectDebateTx(ctx context.Context, fn func(context.Context, app.ObjectDebateRepositories) error) error {
	tx, err := u.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	repos := &failingAuditDebateRepos{
		object: &knowledgeObjectRepository{tx: tx},
		audit:  &failingAuditRepo{err: u.err},
	}
	if err := fn(ctx, repos); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

type failingAuditDebateRepos struct {
	object *knowledgeObjectRepository
	audit  *failingAuditRepo
}

func (r *failingAuditDebateRepos) Objects() app.ObjectDebateObjectRepository { return r.object }
func (r *failingAuditDebateRepos) AuditEvents() app.AuditEventRepository     { return r.audit }
