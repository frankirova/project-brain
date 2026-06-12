package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ----------------------------------------------------------------------------
// Read-path integration tests for ObjectDebateService.ListHumanBacklog
// and backlogQuery.List.
//
// These tests exercise the live Postgres on port 5433 and are gated
// by the PROJECT_BRAIN_TEST_DATABASE_DSN env var (see
// openIntegrationDB). The backlog SQL projection depends on the
// partial index idx_knowledge_objects_debating from migration 0012,
// which is applied automatically by openIntegrationDB.
// ----------------------------------------------------------------------------

// TestPostgresBacklogFiltersByWorkspaceAndIncludesStatusMix covers
// two spec scenarios at once because the test cost is dominated by
// the service round-trip: a non-empty workspace returns only rows
// from that workspace, and rows of every allowed status (proposed,
// debating, recently-deprecated within 14d) all show up.
func TestPostgresBacklogFiltersByWorkspaceAndIncludesStatusMix(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceA := "workspace-a-" + uuid.NewString()
	workspaceB := "workspace-b-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceA) })
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceB) })

	// Workspace A: one of each allowed status. All three must show
	// up. We use UpdateStatus (which bumps updated_at=now) to land
	// the debating and recently-deprecated rows so the 14-day
	// recency window includes them.
	idProposed := seedKnowledgeObject(t, db, workspaceA, domain.KnowledgeObjectStatusProposed)
	idDebating := seedKnowledgeObject(t, db, workspaceA, domain.KnowledgeObjectStatusProposed)
	flipToDebating(t, db, workspaceA, idDebating)
	idRecentlyDeprecated := seedKnowledgeObject(t, db, workspaceA, domain.KnowledgeObjectStatusProposed)
	flipStatus(t, db, workspaceA, idRecentlyDeprecated, domain.KnowledgeObjectStatusDeprecated)

	// Workspace B: an object that must NOT leak into A's backlog.
	idB := seedKnowledgeObject(t, db, workspaceB, domain.KnowledgeObjectStatusProposed)
	flipToDebating(t, db, workspaceB, idB)

	svc := app.NewObjectDebateService(db, NewBacklogQuery(db.pool))
	page, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceA, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog: %v", err)
	}

	gotIDs := backlogIDs(page.Items)
	wantInA := map[uuid.UUID]bool{idProposed: true, idDebating: true, idRecentlyDeprecated: true}
	for id := range wantInA {
		if !gotIDs[id] {
			t.Errorf("workspace A backlog missing object %s; got IDs = %v", id, mapKeysToStrings(gotIDs))
		}
	}
	if gotIDs[idB] {
		t.Errorf("workspace B object %s leaked into workspace A's backlog", idB)
	}
	if page.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty on a single-page response", page.NextCursor)
	}
}

// TestPostgresBacklogExcludesOldDeprecated confirms the 14-day
// recency window: an object deprecated more than 14 days ago must
// NOT appear in the backlog. We seed it directly with an explicit
// updated_at because the repository's UpdateStatus would bump
// updated_at to now().
func TestPostgresBacklogExcludesOldDeprecated(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	// Recently-deprecated: 5 days ago (must show up).
	recentID := seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
	flipStatus(t, db, workspaceID, recentID, domain.KnowledgeObjectStatusDeprecated)

	// Old-deprecated: 30 days ago (must NOT show up). Direct insert
	// so updated_at is fixed, then a direct UPDATE to flip status
	// without bumping updated_at.
	oldID := uuid.New()
	oldUpdatedAt := time.Now().UTC().Add(-30 * 24 * time.Hour)
	seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDeprecated, oldUpdatedAt, oldID)

	svc := app.NewObjectDebateService(db, NewBacklogQuery(db.pool))
	page, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog: %v", err)
	}

	ids := backlogIDs(page.Items)
	if !ids[recentID] {
		t.Errorf("recently-deprecated object %s missing from backlog", recentID)
	}
	if ids[oldID] {
		t.Errorf("old-deprecated object %s appeared in backlog; expected 14-day recency exclusion", oldID)
	}
}

// TestPostgresBacklogDerivedStaleMarker pins the spec SQL
// projection for is_stale and stale_for_days across three cases:
//   - debating + updated_at 20d ago → is_stale=true, stale_for_days=20
//   - debating + updated_at 3d ago  → is_stale=false, stale_for_days=3
//     (the day count is the elapsed days; not clamped because it is
//     already non-negative; only the FUTURE case exercises the clamp)
//   - debating + updated_at in the future (clock skew) →
//     stale_for_days clamped to 0 (the GREATEST(..., 0) guard)
func TestPostgresBacklogDerivedStaleMarker(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	now := time.Now().UTC()
	idStale := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, now.Add(-20*24*time.Hour), uuid.New())
	idFresh := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, now.Add(-3*24*time.Hour), uuid.New())
	idFuture := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, now.Add(24*time.Hour), uuid.New())

	svc := app.NewObjectDebateService(db, NewBacklogQuery(db.pool))
	page, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog: %v", err)
	}

	byID := make(map[uuid.UUID]app.BacklogItem, len(page.Items))
	for _, it := range page.Items {
		byID[it.ID] = it
	}
	cases := []struct {
		id        uuid.UUID
		name      string
		wantStale bool
		wantDays  int
	}{
		{idStale, "stale (20d)", true, 20},
		{idFresh, "fresh (3d)", false, 3},
		{idFuture, "future (clamped to 0)", false, 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			item, ok := byID[tt.id]
			if !ok {
				t.Fatalf("object %s missing from backlog", tt.id)
			}
			if item.IsStale != tt.wantStale {
				t.Errorf("IsStale = %v, want %v", item.IsStale, tt.wantStale)
			}
			if item.StaleForDays != tt.wantDays {
				t.Errorf("StaleForDays = %d, want %d", item.StaleForDays, tt.wantDays)
			}
		})
	}
}

// TestPostgresBacklogOrdersByUpdatedAtDescIDDesc verifies the
// ORDER BY (updated_at DESC, id DESC) guarantee plus the
// tie-breaker: when two rows share updated_at, id DESC resolves
// the order. We seed 4 debating objects at distinct
// (updated_at, id) tuples and check the resulting order.
func TestPostgresBacklogOrdersByUpdatedAtDescIDDesc(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	base := time.Now().UTC().Add(-10 * 24 * time.Hour)
	// Two rows share the SAME updated_at; the second seeded gets a
	// higher UUID (uuid.New is random but increasing) and therefore
	// must come first in the (updated_at DESC, id DESC) ordering.
	shared := base
	older := base.Add(-1 * time.Hour)
	oldest := base.Add(-2 * time.Hour)
	newest := base.Add(1 * time.Hour)

	idNewest := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, newest, uuid.New())
	idSharedB := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, shared, uuid.New())
	idSharedA := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, shared, uuid.New())
	idOlder := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, older, uuid.New())
	idOldest := seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, oldest, uuid.New())

	// Sanity: idSharedB > idSharedA (random UUIDs — we cannot
	// guarantee this without explicit ordering, so swap if needed).
	if idSharedA.String() > idSharedB.String() {
		idSharedA, idSharedB = idSharedB, idSharedA
	}

	svc := app.NewObjectDebateService(db, NewBacklogQuery(db.pool))
	page, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog: %v", err)
	}
	gotIDs := make([]uuid.UUID, 0, len(page.Items))
	for _, it := range page.Items {
		gotIDs = append(gotIDs, it.ID)
	}

	wantPrefix := []uuid.UUID{idNewest, idSharedB, idSharedA, idOlder, idOldest}
	if len(gotIDs) < len(wantPrefix) {
		t.Fatalf("got %d items, want at least %d; got IDs = %v", len(gotIDs), len(wantPrefix), idsToStrings(gotIDs))
	}
	for i, want := range wantPrefix {
		if gotIDs[i] != want {
			t.Errorf("position %d: got %s, want %s; full order = %v", i, gotIDs[i], want, idsToStrings(gotIDs))
		}
	}
}

// TestPostgresBacklogKeysetCursorPaginates covers two spec
// scenarios together: the first page + second page cover all rows
// in order with no overlap, AND the last page emits an empty
// NextCursor. We seed 5 debating objects and ask for pageSize=2,
// expecting first=2+cursor, second=2+cursor, third=1+empty.
func TestPostgresBacklogKeysetCursorPaginates(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	// 5 debating objects at distinct updated_at, newest first.
	base := time.Now().UTC()
	ids := make([]uuid.UUID, 5)
	for i := 0; i < 5; i++ {
		ids[i] = seedKnowledgeObjectAt(t, db, workspaceID, domain.KnowledgeObjectStatusDebating, base.Add(-time.Duration(i)*time.Hour), uuid.New())
	}

	svc := app.NewObjectDebateService(db, NewBacklogQuery(db.pool))

	// First page: 2 items + non-empty NextCursor.
	page1, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog page 1: %v", err)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("page 1: got %d items, want 2", len(page1.Items))
	}
	if page1.NextCursor == "" {
		t.Fatalf("page 1: NextCursor empty, want non-empty (more pages exist)")
	}
	if page1.Items[0].ID != ids[0] || page1.Items[1].ID != ids[1] {
		t.Errorf("page 1 order = %v, want [%s, %s]", mapKeysToStrings(backlogIDs(page1.Items)), ids[0], ids[1])
	}

	// Second page: 2 more items, no overlap with page 1, NextCursor non-empty.
	page2, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 2, Cursor: page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog page 2: %v", err)
	}
	if len(page2.Items) != 2 {
		t.Fatalf("page 2: got %d items, want 2", len(page2.Items))
	}
	if page2.NextCursor == "" {
		t.Fatalf("page 2: NextCursor empty, want non-empty (one row remains)")
	}
	seen := backlogIDs(page1.Items)
	for _, it := range page2.Items {
		if seen[it.ID] {
			t.Errorf("page 2 repeated an object from page 1: %s", it.ID)
		}
		seen[it.ID] = true
	}
	if page2.Items[0].ID != ids[2] || page2.Items[1].ID != ids[3] {
		t.Errorf("page 2 order = %v, want [%s, %s]", mapKeysToStrings(backlogIDs(page2.Items)), ids[2], ids[3])
	}

	// Third page: 1 remaining item, NextCursor empty (last page).
	page3, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 2, Cursor: page2.NextCursor,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog page 3: %v", err)
	}
	if len(page3.Items) != 1 {
		t.Fatalf("page 3: got %d items, want 1", len(page3.Items))
	}
	if page3.NextCursor != "" {
		t.Errorf("page 3 (last): NextCursor = %q, want empty", page3.NextCursor)
	}
	if page3.Items[0].ID != ids[4] {
		t.Errorf("page 3: got %s, want %s", page3.Items[0].ID, ids[4])
	}
	if seen[page3.Items[0].ID] {
		t.Errorf("page 3 repeated an object from a prior page: %s", page3.Items[0].ID)
	}
}

// TestPostgresBacklogPageSizeClamping covers both clamping
// directions in a single test: pageSize=0 must default to 25
// (BacklogDefaultPageSize) and pageSize=999 must clamp to 100
// (BacklogMaxPageSize). We seed 30 proposed objects; both clamped
// queries are expected to return them all with no NextCursor.
func TestPostgresBacklogPageSizeClamping(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	for i := 0; i < 30; i++ {
		seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusProposed)
	}

	svc := app.NewObjectDebateService(db, NewBacklogQuery(db.pool))

	// pageSize=0 → default 25. 30 rows available; 25 returned, NextCursor set.
	page, err := svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 0,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog pageSize=0: %v", err)
	}
	if len(page.Items) != 25 {
		t.Errorf("pageSize=0: got %d items, want 25 (default)", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Errorf("pageSize=0: NextCursor empty, want non-empty (5 more rows)")
	}

	// pageSize=999 → clamp to 100. 30 rows < 100, so all 30 returned, no NextCursor.
	page, err = svc.ListHumanBacklog(context.Background(), app.BacklogFilter{
		WorkspaceID: workspaceID, PageSize: 999,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog pageSize=999: %v", err)
	}
	if len(page.Items) != 30 {
		t.Errorf("pageSize=999: got %d items, want 30 (all rows fit in clamped limit)", len(page.Items))
	}
	if page.NextCursor != "" {
		t.Errorf("pageSize=999: NextCursor = %q, want empty (all rows returned)", page.NextCursor)
	}
}

// TestPostgresBacklogUsesDebatingPartialIndex asserts the planner
// CAN use idx_knowledge_objects_debating (migration 0012) for the
// status='debating' subset. The test disables seqscan inside a
// transaction so the planner is forced to pick the partial index
// when one is available; without the index, the query would have
// no plan and EXPLAIN would error out. This is the standard "is
// the index reachable?" check, independent of whether the planner
// would actually pick the index for a small test dataset.
func TestPostgresBacklogUsesDebatingPartialIndex(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })
	seedKnowledgeObject(t, db, workspaceID, domain.KnowledgeObjectStatusDebating)

	ctx := context.Background()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("SET LOCAL enable_seqscan: %v", err)
	}

	// EXPLAIN returns one row per plan node. The leaf node is
	// where the index name appears, so we must collect every row,
	// not just the first.
	rows, err := tx.Query(ctx, `
EXPLAIN
SELECT id FROM knowledge_objects
WHERE workspace_id = $1 AND status = 'debating'
ORDER BY updated_at DESC, id DESC
LIMIT 26`, workspaceID)
	if err != nil {
		t.Fatalf("EXPLAIN rows: %v", err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("EXPLAIN scan: %v", err)
		}
		plan += line + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN rows.Err: %v", err)
	}
	if !strings.Contains(plan, "idx_knowledge_objects_debating") {
		t.Errorf("EXPLAIN plan does not mention idx_knowledge_objects_debating; plan = %s", plan)
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// seedKnowledgeObjectAt is a copy of seedKnowledgeObject with a
// caller-supplied updated_at and a caller-supplied object ID. Used
// by the read-path tests to position rows on the (updated_at, id)
// timeline — UpdateStatus would bump updated_at to now() and break
// the stale/old-deprecated/ordering tests.
//
// This helper lives in this test file (not in
// object_validation_integration_test.go) because the user prompt
// explicitly forbids touching PR 1/2/3 test files. Mirroring the
// shape of seedKnowledgeObject keeps the call sites uniform.
func seedKnowledgeObjectAt(t *testing.T, db *DB, workspaceID string, status string, updatedAt time.Time, objectID uuid.UUID) uuid.UUID {
	t.Helper()
	now := updatedAt
	_, err := db.pool.Exec(context.Background(), `
INSERT INTO knowledge_objects (id, workspace_id, type, title, summary, content, status, metadata, created_by, created_at, updated_at, tags)
VALUES ($1, $2, $3, $4, $5, $6, $7, '{}'::jsonb, $8, $9, $10, '{}')`,
		objectID, workspaceID, domain.KnowledgeObjectTypeDocument, "Backlog object", "", "content", status, "tester", now, now,
	)
	if err != nil {
		t.Fatalf("seed knowledge object at %s: %v", updatedAt, err)
	}
	return objectID
}

// flipStatus runs a bare UPDATE so updated_at is not touched. Used
// to land a "deprecated" row whose updated_at is 30 days in the
// past, which UpdateStatus (which sets updated_at=now()) cannot
// produce.
func flipStatus(t *testing.T, db *DB, workspaceID string, objectID uuid.UUID, status string) {
	t.Helper()
	if _, err := db.pool.Exec(context.Background(),
		`UPDATE knowledge_objects SET status = $3 WHERE workspace_id = $1 AND id = $2`,
		workspaceID, objectID, status); err != nil {
		t.Fatalf("flip status to %s: %v", status, err)
	}
}

// flipToDebating drives a seeded 'proposed' object through the
// service's MarkDebating so updated_at is bumped to now(). This
// keeps the resulting row inside the 14-day "recently-deprecated"
// or "still debating" windows without bypassing the audit trail.
func flipToDebating(t *testing.T, db *DB, workspaceID string, objectID uuid.UUID) {
	t.Helper()
	svc := app.NewObjectDebateService(db, nil)
	if _, err := svc.MarkDebating(context.Background(), app.MarkDebatingRequest{
		WorkspaceID: workspaceID, ObjectID: objectID,
		TriggeredBy: domain.DebateTriggerHuman, SuggestedBy: "",
		ActorID: "tester", Reason: "seed helper",
	}); err != nil {
		t.Fatalf("flip to debating: %v", err)
	}
}

// backlogIDs returns a set of object IDs from a BacklogPage,
// convenience for membership assertions in the read-path tests.
func backlogIDs(items []app.BacklogItem) map[uuid.UUID]bool {
	out := make(map[uuid.UUID]bool, len(items))
	for _, it := range items {
		out[it.ID] = true
	}
	return out
}

// idsToStrings renders a slice of UUIDs as their canonical
// string form for failure messages.
func idsToStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

// mapKeysToStrings renders the keys of a UUID set as a sorted-ish
// slice of strings for readable failure messages.
func mapKeysToStrings(ids map[uuid.UUID]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id.String())
	}
	return out
}
