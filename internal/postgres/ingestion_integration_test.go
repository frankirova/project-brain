package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresIngestionPersistsAndDeduplicates(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	service := app.NewIngestTextService(db)
	request := domain.IngestTextRequest{
		WorkspaceID: workspaceID,
		Content:     "persistent knowledge",
		Source: domain.SourceInput{
			Type:           "test",
			ExternalID:     "message-1",
			IdempotencyKey: "same-request",
			Metadata:       domain.Metadata{"channel": "integration"},
		},
		Object: domain.ObjectInput{
			Type:      "decision",
			Title:     "Integration decision",
			Summary:   "Stored through PostgreSQL",
			CreatedBy: "tester",
			Metadata:  domain.Metadata{"importance": "high"},
		},
	}

	first, err := service.Ingest(context.Background(), request)
	if err != nil {
		t.Fatalf("first Ingest() returned error: %v", err)
	}
	if first.Duplicate {
		t.Fatal("first Ingest() duplicate = true, want false")
	}
	assertWorkspaceCounts(t, db.pool, workspaceID, recordCounts{sources: 1, objects: 1, links: 1, audits: 1})

	duplicateRequest := request
	duplicateRequest.Content = "different retry content must not overwrite persisted checksum"
	second, err := service.Ingest(context.Background(), duplicateRequest)
	if err != nil {
		t.Fatalf("duplicate Ingest() returned error: %v", err)
	}
	if !second.Duplicate {
		t.Fatal("duplicate Ingest() duplicate = false, want true")
	}
	if second.SourceID != first.SourceID || second.ObjectID != first.ObjectID || second.AuditEventID != first.AuditEventID {
		t.Fatalf("duplicate result IDs = %+v, want original %+v", second, first)
	}
	if second.ContentChecksum != first.ContentChecksum || second.IdentityKey != first.IdentityKey {
		t.Fatalf("duplicate checksum/identity = %q/%q, want persisted %q/%q", second.ContentChecksum, second.IdentityKey, first.ContentChecksum, first.IdentityKey)
	}
	assertWorkspaceCounts(t, db.pool, workspaceID, recordCounts{sources: 1, objects: 1, links: 1, audits: 1})
}

func TestPostgresIngestionRollsBackPartialWrites(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupWorkspace(t, db.pool, workspaceID) })

	failure := errors.New("stop before audit")
	err := db.WithinIngestionTx(context.Background(), func(ctx context.Context, repos app.IngestionRepositories) error {
		sourceID := uuid.New()
		objectID := uuid.New()
		now := time.Now().UTC()

		if err := repos.Sources().Create(ctx, domain.Source{
			ID:          sourceID,
			WorkspaceID: workspaceID,
			Type:        domain.SourceTypeText,
			Checksum:    "rollback-checksum",
			IdentityKey: "rollback-identity",
			CapturedAt:  now,
		}); err != nil {
			return err
		}
		if err := repos.KnowledgeObjects().Create(ctx, domain.KnowledgeObject{
			ID:          objectID,
			WorkspaceID: workspaceID,
			Type:        domain.KnowledgeObjectTypeDocument,
			Content:     "will be rolled back",
			Status:      domain.KnowledgeObjectStatusActive,
			CreatedAt:   now,
			UpdatedAt:   now,
		}); err != nil {
			return err
		}
		if err := repos.ObjectSources().Create(ctx, domain.ObjectSource{ObjectID: objectID, SourceID: sourceID, Relevance: 1}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("WithinIngestionTx() error = %v, want rollback failure", err)
	}
	assertWorkspaceCounts(t, db.pool, workspaceID, recordCounts{})
}

func openIntegrationDB(t *testing.T) *DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping PostgreSQL integration test in short mode")
	}
	dsn := os.Getenv("PROJECT_BRAIN_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("PROJECT_BRAIN_TEST_DATABASE_DSN is unset")
	}

	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	t.Cleanup(db.Close)

	migration, err := os.ReadFile(findMigrationPath(t))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return db
}

func findMigrationPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "migrations", "0001_knowledge_core_ingestion.sql")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("migration file not found from %s", dir)
		}
		dir = parent
	}
}

type recordCounts struct {
	sources int
	objects int
	links   int
	audits  int
}

func assertWorkspaceCounts(t *testing.T, pool *pgxpool.Pool, workspaceID string, want recordCounts) {
	t.Helper()
	ctx := context.Background()
	got := recordCounts{}
	queries := []struct {
		name  string
		query string
		out   *int
	}{
		{"sources", "SELECT count(*) FROM sources WHERE workspace_id = $1", &got.sources},
		{"objects", "SELECT count(*) FROM knowledge_objects WHERE workspace_id = $1", &got.objects},
		{"links", `SELECT count(*) FROM object_sources os JOIN sources s ON s.id = os.source_id WHERE s.workspace_id = $1`, &got.links},
		{"audits", "SELECT count(*) FROM audit_events WHERE workspace_id = $1", &got.audits},
	}
	for _, query := range queries {
		if err := pool.QueryRow(ctx, query.query, workspaceID).Scan(query.out); err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
	}
	if got != want {
		t.Fatalf("record counts = %+v, want %+v", got, want)
	}
}

func cleanupWorkspace(t *testing.T, pool *pgxpool.Pool, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		"DELETE FROM audit_events WHERE workspace_id = $1",
		`DELETE FROM object_sources os USING sources s WHERE os.source_id = s.id AND s.workspace_id = $1`,
		"DELETE FROM knowledge_objects WHERE workspace_id = $1",
		"DELETE FROM sources WHERE workspace_id = $1",
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement, workspaceID); err != nil {
			t.Fatalf("cleanup workspace: %v", err)
		}
	}
}
