package postgres

import (
	"context"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// TestFTSSearchHandlesNullTextColumns is a regression test for the bug
// where Search and FindByID scanned nullable columns (title, summary,
// created_by) into non-nullable Go strings, failing with a scan error
// (surfacing as HTTP 500) whenever a matched row had NULL in any of
// them. An ingest with no title/summary/created_by leaves those columns
// NULL, which is exactly how the bug was found in manual testing.
func TestFTSSearchHandlesNullTextColumns(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	service := app.NewIngestTextService(db)
	res, err := service.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: workspaceID,
		Content:     "zxcvbnmqaz unique searchable token",
		// No Title, Summary, or CreatedBy: those columns end up NULL.
		Object: domain.ObjectInput{Type: "decision"},
	})
	if err != nil {
		t.Fatalf("Ingest() returned error: %v", err)
	}

	retriever := NewFTSRetriever(db.pool)

	// Search must not fail on the NULL columns and must find the object.
	hits, err := retriever.Search(context.Background(), app.SearchQuery{
		Text:        "zxcvbnmqaz",
		WorkspaceID: workspaceID,
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("Search() returned error on NULL columns: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search() returned %d hits, want 1", len(hits))
	}
	if got := hits[0].Object; got.Title != "" || got.Summary != "" || got.CreatedBy != "" {
		t.Errorf("NULL columns should scan as empty strings, got title=%q summary=%q created_by=%q",
			got.Title, got.Summary, got.CreatedBy)
	}

	// FindByID must not fail on the NULL columns either (it is the
	// hydrator used by the composite/vector retriever).
	obj, err := retriever.FindByID(context.Background(), workspaceID, res.ObjectID)
	if err != nil {
		t.Fatalf("FindByID() returned error on NULL columns: %v", err)
	}
	if obj.ID != res.ObjectID {
		t.Fatalf("FindByID() returned wrong object: got %s, want %s", obj.ID, res.ObjectID)
	}
}
