package postgres

import (
	"context"
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
