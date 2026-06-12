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
// fakes (mirror of fakeDebateUOW)
// ----------------------------------------------------------------------------

// fakeBacklogQuery implements BacklogQuery for unit tests. It
// returns a canned page and records the last filter it saw so
// tests can assert the service's pre-processing (workspace
// normalization, page-size clamping, cursor pass-through).
type fakeBacklogQuery struct {
	page     BacklogPage
	err      error
	lastSeen BacklogFilter
	calls    int
}

func (q *fakeBacklogQuery) List(_ context.Context, filter BacklogFilter) (BacklogPage, error) {
	q.calls++
	q.lastSeen = filter
	if q.err != nil {
		return BacklogPage{}, q.err
	}
	return q.page, nil
}

func backlogItemFixture(id uuid.UUID, status string, updatedAt time.Time, isStale bool, staleForDays int) BacklogItem {
	return BacklogItem{
		ID:           id,
		WorkspaceID:  "workspace-1",
		Type:         domain.KnowledgeObjectTypeDocument,
		Title:        "title-" + id.String()[:8],
		Summary:      "summary-" + id.String()[:8],
		Status:       status,
		UpdatedAt:    updatedAt,
		IsStale:      isStale,
		StaleForDays: staleForDays,
	}
}

// newBacklogServiceWithFakeQuery wires ObjectDebateService with a
// fake BacklogQuery. The UoW is set to a minimal valid fake so a
// stray dereference on the read path would fail fast instead of
// nil-panicking; the read path itself does not touch the UoW.
func newBacklogServiceWithFakeQuery(query BacklogQuery) *ObjectDebateService {
	uow := newFakeDebateUOW(domain.KnowledgeObject{})
	return NewObjectDebateServiceWithDependencies(uow, query, uuid.New, time.Now)
}

// ----------------------------------------------------------------------------
// Happy paths
// ----------------------------------------------------------------------------

// TestListHumanBacklogFirstPage: caller asks for the first page;
// the query returns pageSize+1 (the +1 keyset sentinel); the
// service trims to pageSize and emits a NextCursor pointing at
// the LAST VISIBLE row, not the dropped sentinel.
func TestListHumanBacklogFirstPage(t *testing.T) {
	const pageSize = 5
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	items := make([]BacklogItem, pageSize+1)
	for i := 0; i < pageSize+1; i++ {
		items[i] = backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now.Add(-time.Duration(i)*time.Hour), false, 0)
	}
	query := &fakeBacklogQuery{page: BacklogPage{Items: items, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	got, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    pageSize,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if len(got.Items) != pageSize {
		t.Fatalf("len(Items) = %d, want %d (trimmed to pageSize)", len(got.Items), pageSize)
	}
	if got.NextCursor == "" {
		t.Fatalf("NextCursor is empty, want a non-empty cursor on a page that has a follow-up")
	}
	// Round-trip the emitted cursor: it MUST decode to the last
	// VISIBLE item, not the +1 sentinel the service dropped.
	curTime, curID, err := DecodeBacklogCursor(got.NextCursor)
	if err != nil {
		t.Fatalf("DecodeBacklogCursor returned error: %v", err)
	}
	last := got.Items[pageSize-1]
	if !curTime.Equal(last.UpdatedAt) || curID != last.ID {
		t.Fatalf("NextCursor decodes to (%v, %v), want last item (%v, %v)", curTime, curID, last.UpdatedAt, last.ID)
	}
	if query.lastSeen.PageSize != pageSize {
		t.Fatalf("query.lastSeen.PageSize = %d, want %d (no clamp on in-range input)", query.lastSeen.PageSize, pageSize)
	}
}

// TestListHumanBacklogSecondPageViaCursor: caller passes the
// cursor from page 1; the query returns another full page plus a
// sentinel; the service emits a NextCursor again. Asserts the
// cursor was passed through to the fake verbatim.
func TestListHumanBacklogSecondPageViaCursor(t *testing.T) {
	const pageSize = 3
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	pageOneItems := make([]BacklogItem, pageSize+1)
	for i := 0; i < pageSize+1; i++ {
		pageOneItems[i] = backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now.Add(-time.Duration(i)*time.Hour), false, 0)
	}
	pageOne := &fakeBacklogQuery{page: BacklogPage{Items: pageOneItems, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(pageOne)

	first, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    pageSize,
	})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatalf("first page NextCursor empty, want non-empty")
	}

	pageTwoItems := make([]BacklogItem, pageSize+1)
	for i := 0; i < pageSize+1; i++ {
		pageTwoItems[i] = backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now.Add(-time.Duration(pageSize+i+1)*time.Hour), false, 0)
	}
	pageTwo := &fakeBacklogQuery{page: BacklogPage{Items: pageTwoItems, NextCursor: ""}}
	serviceTwo := newBacklogServiceWithFakeQuery(pageTwo)

	second, err := serviceTwo.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    pageSize,
		Cursor:      first.NextCursor,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Items) != pageSize {
		t.Fatalf("second page len(Items) = %d, want %d", len(second.Items), pageSize)
	}
	if second.NextCursor == "" {
		t.Fatalf("second page NextCursor empty, want non-empty")
	}
	if pageTwo.lastSeen.Cursor != first.NextCursor {
		t.Fatalf("query.lastSeen.Cursor = %q, want %q", pageTwo.lastSeen.Cursor, first.NextCursor)
	}
}

// TestListHumanBacklogLastPartialPage: the query returns fewer
// than pageSize rows. The service MUST emit an empty NextCursor so
// the caller stops paginating.
func TestListHumanBacklogLastPartialPage(t *testing.T) {
	const pageSize = 5
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	items := []BacklogItem{
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now, false, 0),
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now.Add(-time.Hour), false, 0),
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusProposed, now.Add(-2*time.Hour), false, 0),
	}
	query := &fakeBacklogQuery{page: BacklogPage{Items: items, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	got, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    pageSize,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if len(got.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3", len(got.Items))
	}
	if got.NextCursor != "" {
		t.Fatalf("NextCursor = %q, want empty on the last page", got.NextCursor)
	}
}

// ----------------------------------------------------------------------------
// Cursor roundtrip with the real codec
// ----------------------------------------------------------------------------

// TestListHumanBacklogCursorRoundTripWithRealCodec pins the
// service's cursor handling against the real PR 1 codec. Encode +
// pass through the service + decode MUST yield the (updated_at,
// id) of the trimmed page's last item.
func TestListHumanBacklogCursorRoundTripWithRealCodec(t *testing.T) {
	const pageSize = 2
	now := time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC)
	lastID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	items := []BacklogItem{
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now, false, 0),
		backlogItemFixture(lastID, domain.KnowledgeObjectStatusDebating, now.Add(-time.Hour), false, 0),
		// +1 sentinel.
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now.Add(-2*time.Hour), false, 0),
	}
	query := &fakeBacklogQuery{page: BacklogPage{Items: items, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	got, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    pageSize,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if len(got.Items) != pageSize {
		t.Fatalf("len(Items) = %d, want %d", len(got.Items), pageSize)
	}
	curTime, curID, err := DecodeBacklogCursor(got.NextCursor)
	if err != nil {
		t.Fatalf("DecodeBacklogCursor returned error: %v", err)
	}
	if curID != lastID {
		t.Fatalf("decoded id = %v, want %v (last visible item)", curID, lastID)
	}
	if !curTime.Equal(now.Add(-time.Hour)) {
		t.Fatalf("decoded time = %v, want %v (last visible item)", curTime, now.Add(-time.Hour))
	}
}

// ----------------------------------------------------------------------------
// Filter clamping
// ----------------------------------------------------------------------------

// TestListHumanBacklogClampsPageSize: limit 0 defaults to
// BacklogDefaultPageSize, limit > 100 clamps to BacklogMaxPageSize.
// The service clamps BEFORE the SQL LIMIT.
func TestListHumanBacklogClampsPageSize(t *testing.T) {
	cases := []struct {
		name       string
		input      int
		wantPassed int
	}{
		{name: "zero defaults to 25", input: 0, wantPassed: BacklogDefaultPageSize},
		{name: "negative defaults to 25", input: -7, wantPassed: BacklogDefaultPageSize},
		{name: "999 clamps to 100", input: 999, wantPassed: BacklogMaxPageSize},
		{name: "100 stays 100", input: BacklogMaxPageSize, wantPassed: BacklogMaxPageSize},
		{name: "1 stays 1 (lower bound)", input: 1, wantPassed: 1},
		{name: "50 stays 50", input: 50, wantPassed: 50},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			query := &fakeBacklogQuery{page: BacklogPage{Items: nil, NextCursor: ""}}
			service := newBacklogServiceWithFakeQuery(query)

			_, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
				WorkspaceID: "workspace-1",
				PageSize:    tt.input,
			})
			if err != nil {
				t.Fatalf("ListHumanBacklog() error: %v", err)
			}
			if query.lastSeen.PageSize != tt.wantPassed {
				t.Fatalf("query.lastSeen.PageSize = %d, want %d (input was %d)", query.lastSeen.PageSize, tt.wantPassed, tt.input)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// Workspace normalization
// ----------------------------------------------------------------------------

// TestListHumanBacklogNormalizesWorkspaceID: " Workspace-1 " must
// reach the fake as "workspace-1". Mirrors the MarkDebating /
// ResolveDebate normalization contract.
func TestListHumanBacklogNormalizesWorkspaceID(t *testing.T) {
	query := &fakeBacklogQuery{page: BacklogPage{Items: nil, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	_, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "  Workspace-1  ",
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if query.lastSeen.WorkspaceID != "workspace-1" {
		t.Fatalf("query.lastSeen.WorkspaceID = %q, want %q", query.lastSeen.WorkspaceID, "workspace-1")
	}
}

// ----------------------------------------------------------------------------
// Cursor error path
// ----------------------------------------------------------------------------

// TestListHumanBacklogRejectsMalformedCursor: a malformed cursor
// returns ErrInvalidCursor and MUST NOT issue the underlying
// query. The fake's call count is the observable "no DB read"
// guarantee.
func TestListHumanBacklogRejectsMalformedCursor(t *testing.T) {
	query := &fakeBacklogQuery{page: BacklogPage{Items: nil, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	_, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    10,
		Cursor:      "!!!not base64!!!",
	})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("ListHumanBacklog() error = %v, want ErrInvalidCursor", err)
	}
	if query.calls != 0 {
		t.Fatalf("query.calls = %d, want 0 (malformed cursor must short-circuit before the DB read)", query.calls)
	}
}

// TestListHumanBacklogRejectsMalformedCursorVariants covers the
// other malformed-input branches the PR 1 codec handles. Note:
// an empty cursor is the FIRST-PAGE marker (not malformed) and is
// asserted separately in TestListHumanBacklogAcceptsEmptyCursorAsFirstPage.
// Sanity check at the end: a freshly encoded cursor decodes
// successfully, so it is NOT in this case set.
func TestListHumanBacklogRejectsMalformedCursorVariants(t *testing.T) {
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	validID := uuid.New()
	validCursor := EncodeBacklogCursor(now, validID)

	cases := []struct {
		name   string
		cursor string
	}{
		{name: "non-base64", cursor: "!!!not base64!!!"},
		{name: "json missing updated_at", cursor: "eyJpZCI6IjAwMDAwMDAwLTAwMDAtMDAwMC0wMDAwLTAwMDAwMDAwMDAwMyJ9"},
		{name: "json missing id", cursor: "eyJ1cGRhdGVkX2F0IjoiMjAyNi0wNi0xMlQwMDowMDowMFoifQ"},
		{name: "zero uuid", cursor: "eyJ1cGRhdGVkX2F0IjoiMjAyNi0wNi0xMlQwMDowMDowMFoiLCJpZCI6IjAwMDAwMDAwLTAwMDAtMDAwMC0wMDAwLTAwMDAwMDAwMDAwMCJ9"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			query := &fakeBacklogQuery{page: BacklogPage{Items: nil, NextCursor: ""}}
			service := newBacklogServiceWithFakeQuery(query)

			_, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
				WorkspaceID: "workspace-1",
				PageSize:    10,
				Cursor:      tt.cursor,
			})
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("ListHumanBacklog() error = %v, want ErrInvalidCursor", err)
			}
			if query.calls != 0 {
				t.Fatalf("query.calls = %d, want 0", query.calls)
			}
		})
	}
	// Sanity: a valid cursor must reach the fake.
	validQuery := &fakeBacklogQuery{page: BacklogPage{Items: nil, NextCursor: ""}}
	validService := newBacklogServiceWithFakeQuery(validQuery)
	if _, err := validService.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    10,
		Cursor:      validCursor,
	}); err != nil {
		t.Fatalf("valid cursor rejected: %v", err)
	}
	if validQuery.calls != 1 {
		t.Fatalf("valid cursor calls = %d, want 1", validQuery.calls)
	}
}

// TestListHumanBacklogAcceptsEmptyCursorAsFirstPage pins the
// "empty cursor = first page" convention. Cursor="" MUST NOT be
// treated as malformed; the service must pass it through to the
// query (which treats it as "no keyset" in the SQL).
func TestListHumanBacklogAcceptsEmptyCursorAsFirstPage(t *testing.T) {
	query := &fakeBacklogQuery{page: BacklogPage{Items: nil, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	_, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    10,
		Cursor:      "",
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog(empty cursor) error: %v", err)
	}
	if query.calls != 1 {
		t.Fatalf("query.calls = %d, want 1 (empty cursor is the first-page marker, not malformed)", query.calls)
	}
	if query.lastSeen.Cursor != "" {
		t.Fatalf("query.lastSeen.Cursor = %q, want \"\" (passed through)", query.lastSeen.Cursor)
	}
}

// ----------------------------------------------------------------------------
// Underlying-query error passthrough
// ----------------------------------------------------------------------------

// TestListHumanBacklogPropagatesQueryError: the service does not
// swallow errors from the BacklogQuery port. Any non-cursor error
// is returned as-is to the caller.
func TestListHumanBacklogPropagatesQueryError(t *testing.T) {
	failure := errors.New("connection refused")
	query := &fakeBacklogQuery{err: failure}
	service := newBacklogServiceWithFakeQuery(query)

	_, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    10,
	})
	if !errors.Is(err, failure) {
		t.Fatalf("ListHumanBacklog() error = %v, want %v", err, failure)
	}
}

// ----------------------------------------------------------------------------
// Stale marker (mix of stale and non-stale debating)
// ----------------------------------------------------------------------------

// TestListHumanBacklogStaleMarkerComputation covers the derived
// is_stale / stale_for_days projection. The service passes the
// rows through verbatim (the SQL has already projected the
// values). The unit test validates the SHAPE contract; the SQL
// correctness itself is PR 4's integration-test responsibility.
func TestListHumanBacklogStaleMarkerComputation(t *testing.T) {
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	items := []BacklogItem{
		// Fresh debating: is_stale=false, stale_for_days=3.
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now.Add(-3*24*time.Hour), false, 3),
		// Stale debating (20 days): is_stale=true, stale_for_days=20.
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDebating, now.Add(-20*24*time.Hour), true, 20),
		// Proposed (is_stale is false by spec; the marker only fires for debating).
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusProposed, now.Add(-1*time.Hour), false, 0),
		// Recent deprecated (5 days): not stale (only "debating" triggers the marker).
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusDeprecated, now.Add(-5*24*time.Hour), false, 5),
	}
	query := &fakeBacklogQuery{page: BacklogPage{Items: items, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	got, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if len(got.Items) != 4 {
		t.Fatalf("len(Items) = %d, want 4", len(got.Items))
	}
	wants := []struct {
		isStale      bool
		staleForDays int
	}{
		{false, 3},
		{true, 20},
		{false, 0},
		{false, 5},
	}
	for i, want := range wants {
		if got.Items[i].IsStale != want.isStale {
			t.Fatalf("Items[%d].IsStale = %v, want %v", i, got.Items[i].IsStale, want.isStale)
		}
		if got.Items[i].StaleForDays != want.staleForDays {
			t.Fatalf("Items[%d].StaleForDays = %d, want %d", i, got.Items[i].StaleForDays, want.staleForDays)
		}
	}
	if got.NextCursor != "" {
		t.Fatalf("NextCursor = %q, want empty (partial page)", got.NextCursor)
	}
}

// ----------------------------------------------------------------------------
// Empty / proposed-only / cross-workspace / recently-deprecated cases
// ----------------------------------------------------------------------------
//
// These cases are about the BacklogQuery implementation's SQL
// projection (which unit tests with a fake cannot drive). They
// document the spec scenarios the integration tests in PR 4 MUST
// cover, plus the "service-level" assertion the fake CAN make
// (filter pass-through, NextCursor emit logic, page size
// preservation).

// TestListHumanBacklogEmptyWorkspace covers the "empty workspace
// returns empty page" spec scenario at the service layer.
func TestListHumanBacklogEmptyWorkspace(t *testing.T) {
	query := &fakeBacklogQuery{page: BacklogPage{Items: []BacklogItem{}, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	got, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "empty-workspace",
		PageSize:    25,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("len(Items) = %d, want 0", len(got.Items))
	}
	if got.NextCursor != "" {
		t.Fatalf("NextCursor = %q, want empty", got.NextCursor)
	}
	if query.lastSeen.WorkspaceID != "empty-workspace" {
		t.Fatalf("query.lastSeen.WorkspaceID = %q, want %q", query.lastSeen.WorkspaceID, "empty-workspace")
	}
}

// TestListHumanBacklogProposedOnlyWorkspace covers the "no
// debating" spec scenario. The service is workspace-agnostic on
// status; the SQL filter is PR 4's job.
func TestListHumanBacklogProposedOnlyWorkspace(t *testing.T) {
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	items := []BacklogItem{
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusProposed, now, false, 0),
		backlogItemFixture(uuid.New(), domain.KnowledgeObjectStatusProposed, now.Add(-time.Hour), false, 0),
	}
	query := &fakeBacklogQuery{page: BacklogPage{Items: items, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	got, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "proposed-only",
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(got.Items))
	}
	for i, item := range got.Items {
		if item.Status != domain.KnowledgeObjectStatusProposed {
			t.Fatalf("Items[%d].Status = %q, want proposed (no service-side filter should drop proposed)", i, item.Status)
		}
	}
}

// TestListHumanBacklogCrossWorkspaceIsolation covers the spec
// "cross-workspace isolation" scenario at the service layer. Two
// calls with different workspaces see two different fakes; the
// service MUST NOT cross-contaminate the responses.
func TestListHumanBacklogCrossWorkspaceIsolation(t *testing.T) {
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	idA := uuid.New()
	idB := uuid.New()
	pageA := &fakeBacklogQuery{page: BacklogPage{
		Items:      []BacklogItem{backlogItemFixture(idA, domain.KnowledgeObjectStatusDebating, now, false, 0)},
		NextCursor: "",
	}}
	pageB := &fakeBacklogQuery{page: BacklogPage{
		Items:      []BacklogItem{backlogItemFixture(idB, domain.KnowledgeObjectStatusDebating, now, false, 0)},
		NextCursor: "",
	}}
	serviceA := newBacklogServiceWithFakeQuery(pageA)
	serviceB := newBacklogServiceWithFakeQuery(pageB)

	gotA, err := serviceA.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-A",
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("workspace A: %v", err)
	}
	gotB, err := serviceB.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-B",
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("workspace B: %v", err)
	}

	if len(gotA.Items) != 1 || gotA.Items[0].ID != idA {
		t.Fatalf("workspace A returned %+v, want idA=%v", gotA.Items, idA)
	}
	if len(gotB.Items) != 1 || gotB.Items[0].ID != idB {
		t.Fatalf("workspace B returned %+v, want idB=%v", gotB.Items, idB)
	}
	if pageA.lastSeen.WorkspaceID != "workspace-a" {
		t.Fatalf("pageA.lastSeen.WorkspaceID = %q, want %q (normalized)", pageA.lastSeen.WorkspaceID, "workspace-a")
	}
	if pageB.lastSeen.WorkspaceID != "workspace-b" {
		t.Fatalf("pageB.lastSeen.WorkspaceID = %q, want %q (normalized)", pageB.lastSeen.WorkspaceID, "workspace-b")
	}
}

// TestListHumanBacklogRecentlyDeprecatedExclusion documents the
// spec scenario "old deprecated objects are excluded". The fake
// is not capable of filtering by age, so the unit test asserts
// only the service's pass-through behavior. The 14-day recency
// window is enforced at the SQL layer; PR 4 integration tests
// will assert the window is honored end-to-end.
func TestListHumanBacklogRecentlyDeprecatedExclusion(t *testing.T) {
	now := time.Date(2026, 6, 12, 15, 0, 0, 0, time.UTC)
	deprecatedID := uuid.New()
	items := []BacklogItem{
		// Recently deprecated (5 days): would be included by the
		// SQL window.
		backlogItemFixture(deprecatedID, domain.KnowledgeObjectStatusDeprecated, now.Add(-5*24*time.Hour), false, 5),
	}
	query := &fakeBacklogQuery{page: BacklogPage{Items: items, NextCursor: ""}}
	service := newBacklogServiceWithFakeQuery(query)

	got, err := service.ListHumanBacklog(context.Background(), BacklogFilter{
		WorkspaceID: "workspace-1",
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("ListHumanBacklog() error: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1 (recently-deprecated row passes through service unchanged)", len(got.Items))
	}
	if got.Items[0].ID != deprecatedID || got.Items[0].Status != domain.KnowledgeObjectStatusDeprecated {
		t.Fatalf("Items[0] = %+v, want recently-deprecated row", got.Items[0])
	}
}
