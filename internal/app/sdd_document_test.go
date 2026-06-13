package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Phase 4.2 — classifySection table test
// ---------------------------------------------------------------------------

func TestClassifySection(t *testing.T) {
	cases := []struct {
		name    string
		objType string
		wantKey domain.SddSectionKey
	}{
		{"decision -> decisions", domain.KnowledgeObjectTypeDecision, domain.SddSectionDecisions},
		{"constraint -> constraints", domain.KnowledgeObjectTypeConstraint, domain.SddSectionConstraints},
		{"open_question -> open_questions", domain.KnowledgeObjectTypeOpenQuestion, domain.SddSectionOpenQuestions},
		{"document -> context", domain.KnowledgeObjectTypeDocument, domain.SddSectionContext},
		{"unknown type -> context", "diagram", domain.SddSectionContext},
		{"empty string -> context", "", domain.SddSectionContext},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			obj := domain.KnowledgeObject{Type: tt.objType}
			got := classifySection(obj)
			if got != tt.wantKey {
				t.Errorf("classifySection(%q) = %q, want %q", tt.objType, got, tt.wantKey)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Phase 4.3 — AppendValidatedObject (validated path) + GetDocument
// ---------------------------------------------------------------------------

func TestAppendValidatedObject_NewEntry(t *testing.T) {
	repo := newFakeSddRepo()
	svc := NewSddDocumentService(repo, fixedNow, nil)

	obj := domain.KnowledgeObject{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkspaceID: "ws-1",
		Type:        domain.KnowledgeObjectTypeDecision,
		Title:       "Use Postgres",
		Summary:     "We chose Postgres.",
		Status:      domain.KnowledgeObjectStatusValidated,
	}
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("AppendValidatedObject returned error: %v", err)
	}

	doc, err := repo.FindByWorkspace(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("FindByWorkspace returned error: %v", err)
	}
	entries := doc.Sections[domain.SddSectionDecisions]
	if len(entries) != 1 {
		t.Fatalf("len(decisions) = %d, want 1", len(entries))
	}
	if entries[0].ObjectID != obj.ID || entries[0].Title != obj.Title {
		t.Errorf("entry = %+v, want matching object fields", entries[0])
	}
}

func TestAppendValidatedObject_RevalidateSameIDNoDuplicate(t *testing.T) {
	repo := newFakeSddRepo()
	svc := NewSddDocumentService(repo, fixedNow, nil)
	objID := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	obj := domain.KnowledgeObject{
		ID: objID, WorkspaceID: "ws-1",
		Type: domain.KnowledgeObjectTypeDecision, Title: "v1", Summary: "summary v1",
		Status: domain.KnowledgeObjectStatusValidated,
	}
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("first append error: %v", err)
	}

	obj.Title = "v2"
	obj.Summary = "summary v2"
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("second append error: %v", err)
	}

	doc, _ := repo.FindByWorkspace(context.Background(), "ws-1")
	entries := doc.Sections[domain.SddSectionDecisions]
	if len(entries) != 1 {
		t.Fatalf("len(decisions) = %d after re-validate, want 1 (no duplicate)", len(entries))
	}
	if entries[0].Title != "v2" {
		t.Errorf("title = %q, want v2 (replaced)", entries[0].Title)
	}
}

func TestAppendValidatedObject_TypeChangeMovesAcrossSections(t *testing.T) {
	repo := newFakeSddRepo()
	svc := NewSddDocumentService(repo, fixedNow, nil)
	objID := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	// First validate as decision.
	obj := domain.KnowledgeObject{
		ID: objID, WorkspaceID: "ws-1",
		Type: domain.KnowledgeObjectTypeDecision, Title: "T", Summary: "S",
		Status: domain.KnowledgeObjectStatusValidated,
	}
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("first append error: %v", err)
	}

	// Re-validate with type changed to constraint.
	obj.Type = domain.KnowledgeObjectTypeConstraint
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("second append error: %v", err)
	}

	doc, _ := repo.FindByWorkspace(context.Background(), "ws-1")
	if len(doc.Sections[domain.SddSectionDecisions]) != 0 {
		t.Errorf("decisions should be empty after type change, got %d entries", len(doc.Sections[domain.SddSectionDecisions]))
	}
	if len(doc.Sections[domain.SddSectionConstraints]) != 1 {
		t.Errorf("constraints should have 1 entry after type change, got %d", len(doc.Sections[domain.SddSectionConstraints]))
	}
}

func TestGetDocument_ErrNotFoundPropagates(t *testing.T) {
	repo := newFakeSddRepo() // empty — no rows
	svc := NewSddDocumentService(repo, fixedNow, nil)

	_, err := svc.GetDocument(context.Background(), "ws-empty")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDocument error = %v, want ErrNotFound", err)
	}
}

// TestAppendValidatedObject_SortedByUpdatedAtDesc verifies that entries within
// a section are kept in UpdatedAt DESC order.
func TestAppendValidatedObject_SortedByUpdatedAtDesc(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	call := 0
	clk := func() time.Time {
		call++
		if call == 1 {
			return t0
		}
		return t1
	}

	repo := newFakeSddRepo()
	svc := NewSddDocumentService(repo, clk, nil)

	obj1 := domain.KnowledgeObject{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000010"),
		WorkspaceID: "ws-1", Type: domain.KnowledgeObjectTypeDecision,
		Title: "older", Status: domain.KnowledgeObjectStatusValidated,
	}
	obj2 := domain.KnowledgeObject{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000011"),
		WorkspaceID: "ws-1", Type: domain.KnowledgeObjectTypeDecision,
		Title: "newer", Status: domain.KnowledgeObjectStatusValidated,
	}
	_ = svc.AppendValidatedObject(context.Background(), obj1)
	_ = svc.AppendValidatedObject(context.Background(), obj2)

	doc, _ := repo.FindByWorkspace(context.Background(), "ws-1")
	entries := doc.Sections[domain.SddSectionDecisions]
	if len(entries) != 2 {
		t.Fatalf("len(decisions) = %d, want 2", len(entries))
	}
	if entries[0].Title != "newer" {
		t.Errorf("entries[0].Title = %q, want newer (most recent first)", entries[0].Title)
	}
}

// ---------------------------------------------------------------------------
// Phase 4.4 — AppendValidatedObject (deprecated path)
// ---------------------------------------------------------------------------

func TestAppendValidatedObject_DeprecatedRemovesEntry(t *testing.T) {
	repo := newFakeSddRepo()
	svc := NewSddDocumentService(repo, fixedNow, nil)
	objID := uuid.MustParse("00000000-0000-0000-0000-000000000020")

	// Validate first.
	obj := domain.KnowledgeObject{
		ID: objID, WorkspaceID: "ws-1",
		Type: domain.KnowledgeObjectTypeDecision, Title: "D", Summary: "S",
		Status: domain.KnowledgeObjectStatusValidated,
	}
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("validate error: %v", err)
	}

	// Now deprecate.
	obj.Status = domain.KnowledgeObjectStatusDeprecated
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("deprecate error: %v", err)
	}

	doc, _ := repo.FindByWorkspace(context.Background(), "ws-1")
	for _, k := range domain.SddOrderedSections {
		if len(doc.Sections[k]) != 0 {
			t.Errorf("section %q should be empty after deprecation, got %d entries", k, len(doc.Sections[k]))
		}
	}
}

func TestAppendValidatedObject_DeprecateAbsentIsNoOp(t *testing.T) {
	repo := newFakeSddRepo()
	svc := NewSddDocumentService(repo, fixedNow, nil)

	// Seed a different object so the document exists.
	seed := domain.KnowledgeObject{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000030"),
		WorkspaceID: "ws-1", Type: domain.KnowledgeObjectTypeDecision,
		Title: "keep", Status: domain.KnowledgeObjectStatusValidated,
	}
	if err := svc.AppendValidatedObject(context.Background(), seed); err != nil {
		t.Fatalf("seed error: %v", err)
	}

	// Deprecate an object that was never validated.
	absent := domain.KnowledgeObject{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000031"),
		WorkspaceID: "ws-1", Type: domain.KnowledgeObjectTypeDecision,
		Status: domain.KnowledgeObjectStatusDeprecated,
	}
	if err := svc.AppendValidatedObject(context.Background(), absent); err != nil {
		t.Fatalf("deprecate absent error: %v", err)
	}

	doc, _ := repo.FindByWorkspace(context.Background(), "ws-1")
	if len(doc.Sections[domain.SddSectionDecisions]) != 1 {
		t.Errorf("len(decisions) = %d, want 1 (seed untouched)", len(doc.Sections[domain.SddSectionDecisions]))
	}
}

func TestAppendValidatedObject_DeprecateEmptyDocIsNoOp(t *testing.T) {
	repo := newFakeSddRepo() // no rows
	svc := NewSddDocumentService(repo, fixedNow, nil)

	obj := domain.KnowledgeObject{
		ID:          uuid.MustParse("00000000-0000-0000-0000-000000000040"),
		WorkspaceID: "ws-1", Type: domain.KnowledgeObjectTypeDecision,
		Status: domain.KnowledgeObjectStatusDeprecated,
	}
	// Should not error.
	if err := svc.AppendValidatedObject(context.Background(), obj); err != nil {
		t.Fatalf("deprecate on empty doc error: %v", err)
	}
	// No upsert should have been issued (absent entry means no-op).
	if repo.upsertCalled {
		t.Errorf("upsert was called for absent-entry deprecation, want no-op")
	}
}

// ---------------------------------------------------------------------------
// Fake SddDocumentRepository
// ---------------------------------------------------------------------------

type fakeSddRepo struct {
	docs         map[string]domain.SddDocument
	upsertCalled bool
}

func newFakeSddRepo() *fakeSddRepo {
	return &fakeSddRepo{docs: make(map[string]domain.SddDocument)}
}

func (r *fakeSddRepo) FindByWorkspace(_ context.Context, workspaceID string) (domain.SddDocument, error) {
	doc, ok := r.docs[workspaceID]
	if !ok {
		return domain.SddDocument{}, ErrNotFound
	}
	return doc, nil
}

func (r *fakeSddRepo) Upsert(_ context.Context, doc domain.SddDocument) error {
	r.upsertCalled = true
	r.docs[doc.WorkspaceID] = doc
	return nil
}

// Ensure fakeSddRepo satisfies the interface.
var _ SddDocumentRepository = (*fakeSddRepo)(nil)

// fixedNow is a deterministic clock for tests that don't need time variation.
var fixedNow = func() time.Time {
	return time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
}

// ---------------------------------------------------------------------------
// Fake that returns an error on Upsert (for error-path tests)
// ---------------------------------------------------------------------------

type failingUpsertRepo struct {
	*fakeSddRepo
	err error
}

func (r *failingUpsertRepo) Upsert(_ context.Context, _ domain.SddDocument) error {
	return r.err
}

var _ SddDocumentRepository = (*failingUpsertRepo)(nil)

// TestGetDocument_PropagatesRepoError verifies non-ErrNotFound errors bubble up.
func TestGetDocument_PropagatesRepoError(t *testing.T) {
	boom := errors.New("db exploded")
	repo := &failingFindRepo{err: boom}
	svc := NewSddDocumentService(repo, fixedNow, nil)

	_, err := svc.GetDocument(context.Background(), "ws-1")
	if !errors.Is(err, boom) {
		t.Errorf("GetDocument() error = %v, want %v", err, boom)
	}
}

type failingFindRepo struct {
	err error
}

func (r *failingFindRepo) FindByWorkspace(_ context.Context, _ string) (domain.SddDocument, error) {
	return domain.SddDocument{}, r.err
}

func (r *failingFindRepo) Upsert(_ context.Context, _ domain.SddDocument) error {
	return nil
}

var _ SddDocumentRepository = (*failingFindRepo)(nil)
