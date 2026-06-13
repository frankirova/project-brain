package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// TestSddDocumentRepoUpsertAndFindByWorkspace exercises the full Upsert +
// FindByWorkspace round-trip against a live Postgres instance.
//
// The test is gated by PROJECT_BRAIN_TEST_DATABASE_DSN and skipped in short
// mode (testing.Short() == true).
func TestSddDocumentRepoUpsertAndFindByWorkspace(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	repo := NewSddDocumentRepo(db.Pool())

	workspaceID := "sdd-test-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool().Exec(ctx, "DELETE FROM sdd_documents WHERE workspace_id = $1", workspaceID)
	})

	t.Run("unknown workspace returns ErrNotFound", func(t *testing.T) {
		_, err := repo.FindByWorkspace(ctx, "nonexistent-"+uuid.NewString())
		if err != app.ErrNotFound {
			t.Fatalf("want app.ErrNotFound, got %v", err)
		}
	})

	now := time.Now().UTC().Truncate(time.Millisecond)
	objectID := uuid.New()

	initialDoc := domain.SddDocument{
		WorkspaceID: workspaceID,
		Sections: map[domain.SddSectionKey][]domain.SddEntry{
			domain.SddSectionContext: {
				{
					ObjectID:  objectID,
					Title:     "First Decision",
					Summary:   "We chose X.",
					UpdatedAt: now,
				},
			},
			domain.SddSectionDecisions:     {},
			domain.SddSectionConstraints:   {},
			domain.SddSectionOpenQuestions: {},
		},
		UpdatedAt: now,
	}

	t.Run("first Upsert inserts a new row", func(t *testing.T) {
		if err := repo.Upsert(ctx, initialDoc); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		got, err := repo.FindByWorkspace(ctx, workspaceID)
		if err != nil {
			t.Fatalf("FindByWorkspace: %v", err)
		}
		if got.WorkspaceID != workspaceID {
			t.Errorf("WorkspaceID = %q, want %q", got.WorkspaceID, workspaceID)
		}
		ctxEntries := got.Sections[domain.SddSectionContext]
		if len(ctxEntries) != 1 {
			t.Fatalf("context entries = %d, want 1", len(ctxEntries))
		}
		if ctxEntries[0].ObjectID != objectID {
			t.Errorf("ObjectID = %v, want %v", ctxEntries[0].ObjectID, objectID)
		}
		if ctxEntries[0].Title != "First Decision" {
			t.Errorf("Title = %q, want First Decision", ctxEntries[0].Title)
		}
		if ctxEntries[0].Summary != "We chose X." {
			t.Errorf("Summary = %q, want We chose X.", ctxEntries[0].Summary)
		}
	})

	t.Run("second Upsert replaces the row", func(t *testing.T) {
		objectID2 := uuid.New()
		now2 := now.Add(time.Second)
		updatedDoc := domain.SddDocument{
			WorkspaceID: workspaceID,
			Sections: map[domain.SddSectionKey][]domain.SddEntry{
				domain.SddSectionContext: {},
				domain.SddSectionDecisions: {
					{
						ObjectID:  objectID2,
						Title:     "Updated",
						Summary:   "Changed summary.",
						UpdatedAt: now2,
					},
				},
				domain.SddSectionConstraints:   {},
				domain.SddSectionOpenQuestions: {},
			},
			UpdatedAt: now2,
		}

		if err := repo.Upsert(ctx, updatedDoc); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}

		got, err := repo.FindByWorkspace(ctx, workspaceID)
		if err != nil {
			t.Fatalf("FindByWorkspace after second Upsert: %v", err)
		}
		// Decisions section should now have the new entry.
		decEntries := got.Sections[domain.SddSectionDecisions]
		if len(decEntries) != 1 {
			t.Fatalf("decisions entries = %d, want 1", len(decEntries))
		}
		if decEntries[0].ObjectID != objectID2 {
			t.Errorf("ObjectID = %v, want %v", decEntries[0].ObjectID, objectID2)
		}
		// Context section should now be empty.
		if len(got.Sections[domain.SddSectionContext]) != 0 {
			t.Errorf("context entries = %d after replace, want 0", len(got.Sections[domain.SddSectionContext]))
		}
	})

	t.Run("empty sections marshal as empty object, not SQL NULL", func(t *testing.T) {
		emptyDoc := domain.SddDocument{
			WorkspaceID: "sdd-empty-" + uuid.NewString(),
			Sections:    nil, // nil map
			UpdatedAt:   now,
		}
		t.Cleanup(func() {
			_, _ = db.Pool().Exec(ctx, "DELETE FROM sdd_documents WHERE workspace_id = $1", emptyDoc.WorkspaceID)
		})

		if err := repo.Upsert(ctx, emptyDoc); err != nil {
			t.Fatalf("Upsert with nil sections: %v", err)
		}

		got, err := repo.FindByWorkspace(ctx, emptyDoc.WorkspaceID)
		if err != nil {
			t.Fatalf("FindByWorkspace after nil-sections Upsert: %v", err)
		}
		if got.Sections == nil {
			t.Error("Sections should not be nil after round-trip")
		}
	})
}

// TestSddDocumentRepo_ConcurrentAppendPreservesAllEntries is the
// load-bearing regression net for change-16 PR 3. It exercises the
// SddDocumentService through the real *DB.WithinSddDocumentTx
// boundary, with 8 goroutines appending a distinct objectID to the
// same workspace_id in parallel. The SELECT ... FOR UPDATE inside
// the tx-bound repo serializes the read-modify-write; after all
// goroutines join, the final document MUST contain all 8 entries
// — none lost, none duplicated.
//
// The test is gated by PROJECT_BRAIN_TEST_DATABASE_DSN and skipped
// in short mode (testing.Short() == true). It is designed to be run
// with `go test -race` to flag any data races in the service / repo
// code paths.
func TestSddDocumentRepo_ConcurrentAppendPreservesAllEntries(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()

	workspaceID := "sdd-concurrent-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool().Exec(ctx, "DELETE FROM sdd_documents WHERE workspace_id = $1", workspaceID)
	})

	// Seed the row with an empty doc so the FOR UPDATE has a real
	// row to lock. Two concurrent validates on a brand-new
	// workspace would both see ErrNoRows from the lock SELECT and
	// the INSERT...ON CONFLICT inside Upsert would race on the
	// primary key — the design explicitly documents this as
	// "first-writer-wins" for the empty-doc case, which is fine
	// for a brand-new workspace but does not exercise the lock
	// we want to verify. Pre-seeding with a real row is what the
	// contended write path actually looks like in production
	// (a workspace accumulates entries over time).
	now := time.Now().UTC().Truncate(time.Millisecond)
	seed := domain.SddDocument{
		WorkspaceID: workspaceID,
		Sections: map[domain.SddSectionKey][]domain.SddEntry{
			domain.SddSectionContext:       {},
			domain.SddSectionDecisions:     {},
			domain.SddSectionConstraints:   {},
			domain.SddSectionOpenQuestions: {},
		},
		UpdatedAt: now,
	}
	if err := db.WithinSddDocumentTx(ctx, func(ctx context.Context, repo app.SddDocumentRepository) error {
		return repo.Upsert(ctx, seed)
	}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	// Use the SddDocumentService (the production code path) to
	// append — the whole point is to prove the service correctly
	// threads the UoW, not just the raw repo. The clock is fixed
	// so sort order is deterministic.
	svc := app.NewSddDocumentService(db, fixedConcurrencyNow, nil)

	const N = 8
	ids := make([]uuid.UUID, N)
	for i := 0; i < N; i++ {
		base := uuid.MustParse("00000000-0000-0000-0000-000000000000")
		ids[i] = mustConcurrentOffsetUUID(base, i+1)
	}

	var wg sync.WaitGroup
	errs := make(chan error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			obj := domain.KnowledgeObject{
				ID:          ids[i],
				WorkspaceID: workspaceID,
				Type:        domain.KnowledgeObjectTypeDecision,
				Title:       "concurrent-" + string(rune('a'+i)),
				Summary:     "concurrent append",
				Status:      domain.KnowledgeObjectStatusValidated,
			}
			errs <- svc.AppendValidatedObject(ctx, obj)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent AppendValidatedObject returned error: %v", err)
		}
	}

	// Read the final document and assert all 8 entries landed.
	final, err := db.SddDocuments().FindByWorkspace(ctx, workspaceID)
	if err != nil {
		t.Fatalf("FindByWorkspace after concurrent appends: %v", err)
	}
	entries := final.Sections[domain.SddSectionDecisions]
	if len(entries) != N {
		t.Fatalf("len(decisions) = %d, want %d (lost updates detected)", len(entries), N)
	}
	got := make(map[uuid.UUID]bool, N)
	for _, e := range entries {
		if got[e.ObjectID] {
			t.Errorf("duplicate entry in final document: %s", e.ObjectID)
		}
		got[e.ObjectID] = true
	}
	if len(got) != N {
		t.Errorf("distinct entries in final document = %d, want %d", len(got), N)
	}
}

// fixedConcurrencyNow is a deterministic clock for the integration
// concurrent test. All entries get the same UpdatedAt so the
// tiebreaker in sortSectionDesc is exercised (and the test does
// not depend on the wall clock advancing during execution).
func fixedConcurrencyNow() time.Time {
	return time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
}

// mustConcurrentOffsetUUID is a helper that constructs a v4-shaped
// UUID with the last byte set to n. It is duplicated here (the app
// package has the same helper) to avoid an import-only-for-test
// cycle: this file is in package postgres and cannot import a
// non-exported helper from the app test package.
func mustConcurrentOffsetUUID(base uuid.UUID, n int) uuid.UUID {
	var b [16]byte
	copy(b[:], base[:])
	b[15] = byte(n)
	return uuid.Must(uuid.FromBytes(b[:]))
}
