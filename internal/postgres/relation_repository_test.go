package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRelationRepositoryCreateAndFind(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	// Create prerequisite knowledge objects
	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)

	confidence := 0.9
	rel := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeSupports,
		Confidence:     &confidence,
		Evidence:       "corroborated by study X",
		Metadata:       domain.Metadata{"channel": "test"},
		CreatedAt:      now,
	}

	if err := db.Relations().Create(ctx, rel); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	results, err := db.Relations().FindBySourceObjectID(ctx, workspaceID, objA)
	if err != nil {
		t.Fatalf("FindBySourceObjectID() returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("FindBySourceObjectID() returned %d results, want 1", len(results))
	}
	got := results[0]
	if got.ID != rel.ID {
		t.Errorf("ID = %v, want %v", got.ID, rel.ID)
	}
	if got.SourceObjectID != objA {
		t.Errorf("SourceObjectID = %v, want %v", got.SourceObjectID, objA)
	}
	if got.TargetObjectID != objB {
		t.Errorf("TargetObjectID = %v, want %v", got.TargetObjectID, objB)
	}
	if got.RelationType != domain.RelationTypeSupports {
		t.Errorf("RelationType = %q, want %q", got.RelationType, domain.RelationTypeSupports)
	}
	if got.Confidence == nil || *got.Confidence != confidence {
		t.Errorf("Confidence = %v, want %v", got.Confidence, confidence)
	}
	if got.Evidence != "corroborated by study X" {
		t.Errorf("Evidence = %q, want %q", got.Evidence, "corroborated by study X")
	}
}

func TestRelationRepositoryCreateMinimalFields(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)

	rel := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeMentions,
		CreatedAt:      now,
	}

	if err := db.Relations().Create(ctx, rel); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	results, err := db.Relations().FindBySourceObjectID(ctx, workspaceID, objA)
	if err != nil {
		t.Fatalf("FindBySourceObjectID() returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("FindBySourceObjectID() returned %d results, want 1", len(results))
	}
	got := results[0]
	if got.Confidence != nil {
		t.Errorf("Confidence = %v, want nil", *got.Confidence)
	}
	if got.Evidence != "" {
		t.Errorf("Evidence = %q, want empty", got.Evidence)
	}
}

func TestRelationRepositorySelfReferenceRejectedByDB(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)

	rel := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objA,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}

	err := db.Relations().Create(ctx, rel)
	if err == nil {
		t.Fatal("Create() with self-reference succeeded, want error")
	}
}

func TestRelationRepositoryDuplicatePairRejected(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)

	rel := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}

	if err := db.Relations().Create(ctx, rel); err != nil {
		t.Fatalf("first Create() returned error: %v", err)
	}

	dup := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}

	err := db.Relations().Create(ctx, dup)
	if err == nil {
		t.Fatal("duplicate Create() succeeded, want uniqueness violation error")
	}
}

func TestRelationRepositorySamePairDifferentTypeAllowed(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)

	rel1 := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel1); err != nil {
		t.Fatalf("first Create() returned error: %v", err)
	}

	rel2 := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeContradicts,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel2); err != nil {
		t.Fatalf("second Create() with different type returned error: %v", err)
	}

	results, err := db.Relations().FindBySourceObjectID(ctx, workspaceID, objA)
	if err != nil {
		t.Fatalf("FindBySourceObjectID() returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("FindBySourceObjectID() returned %d results, want 2", len(results))
	}
}

func TestRelationRepositoryFindByTarget(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)
	objC := createTestObject(t, ctx, db.pool, workspaceID, now)

	// A -> C
	rel1 := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objC,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel1); err != nil {
		t.Fatalf("Create() rel1 returned error: %v", err)
	}

	// B -> C
	rel2 := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objB,
		TargetObjectID: objC,
		RelationType:   domain.RelationTypeDependsOn,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel2); err != nil {
		t.Fatalf("Create() rel2 returned error: %v", err)
	}

	results, err := db.Relations().FindByTargetObjectID(ctx, workspaceID, objC)
	if err != nil {
		t.Fatalf("FindByTargetObjectID() returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("FindByTargetObjectID() returned %d results, want 2", len(results))
	}
}

func TestRelationRepositoryFindByType(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)
	objC := createTestObject(t, ctx, db.pool, workspaceID, now)
	objD := createTestObject(t, ctx, db.pool, workspaceID, now)

	// A -> B contradicts
	rel1 := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeContradicts,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel1); err != nil {
		t.Fatalf("Create() rel1 returned error: %v", err)
	}

	// C -> D contradicts
	rel2 := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objC,
		TargetObjectID: objD,
		RelationType:   domain.RelationTypeContradicts,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel2); err != nil {
		t.Fatalf("Create() rel2 returned error: %v", err)
	}

	results, err := db.Relations().FindByType(ctx, workspaceID, domain.RelationTypeContradicts)
	if err != nil {
		t.Fatalf("FindByType() returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("FindByType() returned %d results, want 2", len(results))
	}
}

func TestRelationRepositoryWorkspaceIsolation(t *testing.T) {
	db := openIntegrationDB(t)
	ws1 := "workspace-" + uuid.NewString()
	ws2 := "workspace-" + uuid.NewString()
	t.Cleanup(func() {
		cleanupRelationWorkspace(t, db.pool, ws1)
		cleanupRelationWorkspace(t, db.pool, ws2)
	})

	ctx := context.Background()
	now := time.Now().UTC()

	objW1A := createTestObject(t, ctx, db.pool, ws1, now)
	objW1B := createTestObject(t, ctx, db.pool, ws1, now)
	objW2A := createTestObject(t, ctx, db.pool, ws2, now)

	// ws1: A -> B
	rel := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    ws1,
		SourceObjectID: objW1A,
		TargetObjectID: objW1B,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	// Query from ws2 with same source object ID (doesn't exist in ws2)
	results, err := db.Relations().FindBySourceObjectID(ctx, ws2, objW2A)
	if err != nil {
		t.Fatalf("FindBySourceObjectID() returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("FindBySourceObjectID() in different workspace returned %d results, want 0", len(results))
	}

	// Query by type in ws2 — should be empty
	results, err = db.Relations().FindByType(ctx, ws2, domain.RelationTypeSupports)
	if err != nil {
		t.Fatalf("FindByType() returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("FindByType() in different workspace returned %d results, want 0", len(results))
	}
}

func TestRelationRepositoryCascadeDeleteSource(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)

	rel := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	// Delete source object — cascade should remove the relation
	if _, err := db.pool.Exec(ctx, "DELETE FROM knowledge_objects WHERE id = $1", objA); err != nil {
		t.Fatalf("delete source object: %v", err)
	}

	results, err := db.Relations().FindBySourceObjectID(ctx, workspaceID, objA)
	if err != nil {
		t.Fatalf("FindBySourceObjectID() returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("FindBySourceObjectID() after cascade delete returned %d results, want 0", len(results))
	}
}

func TestRelationRepositoryCascadeDeleteTarget(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)

	rel := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, rel); err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	// Delete target object — cascade should remove the relation
	if _, err := db.pool.Exec(ctx, "DELETE FROM knowledge_objects WHERE id = $1", objB); err != nil {
		t.Fatalf("delete target object: %v", err)
	}

	results, err := db.Relations().FindByTargetObjectID(ctx, workspaceID, objB)
	if err != nil {
		t.Fatalf("FindByTargetObjectID() returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("FindByTargetObjectID() after cascade delete returned %d results, want 0", len(results))
	}
}

func TestRelationRepositoryUnrelatedRelationsPreserved(t *testing.T) {
	db := openIntegrationDB(t)
	workspaceID := "workspace-" + uuid.NewString()
	t.Cleanup(func() { cleanupRelationWorkspace(t, db.pool, workspaceID) })

	ctx := context.Background()
	now := time.Now().UTC()

	objA := createTestObject(t, ctx, db.pool, workspaceID, now)
	objB := createTestObject(t, ctx, db.pool, workspaceID, now)
	objC := createTestObject(t, ctx, db.pool, workspaceID, now)
	objD := createTestObject(t, ctx, db.pool, workspaceID, now)

	// A -> B
	relAB := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objA,
		TargetObjectID: objB,
		RelationType:   domain.RelationTypeSupports,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, relAB); err != nil {
		t.Fatalf("Create() relAB returned error: %v", err)
	}

	// C -> D
	relCD := domain.Relation{
		ID:             uuid.New(),
		WorkspaceID:    workspaceID,
		SourceObjectID: objC,
		TargetObjectID: objD,
		RelationType:   domain.RelationTypeDependsOn,
		CreatedAt:      now,
	}
	if err := db.Relations().Create(ctx, relCD); err != nil {
		t.Fatalf("Create() relCD returned error: %v", err)
	}

	// Delete A — should only remove A->B
	if _, err := db.pool.Exec(ctx, "DELETE FROM knowledge_objects WHERE id = $1", objA); err != nil {
		t.Fatalf("delete object A: %v", err)
	}

	// C->D should still exist
	results, err := db.Relations().FindBySourceObjectID(ctx, workspaceID, objC)
	if err != nil {
		t.Fatalf("FindBySourceObjectID() returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("FindBySourceObjectID() after unrelated delete returned %d results, want 1", len(results))
	}
	if results[0].TargetObjectID != objD {
		t.Errorf("remaining relation target = %v, want %v", results[0].TargetObjectID, objD)
	}
}

// --- helpers ---

func createTestObject(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string, now time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(ctx, `
INSERT INTO knowledge_objects (id, workspace_id, type, title, summary, content, status, metadata, created_by, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11)`,
		id,
		workspaceID,
		"document",
		"test object",
		"test summary",
		"test content",
		"active",
		"{}",
		"test",
		now,
		now,
	)
	if err != nil {
		t.Fatalf("createTestObject: %v", err)
	}
	return id
}

func cleanupRelationWorkspace(t *testing.T, pool *pgxpool.Pool, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	// Relations cascade-delete with knowledge_objects, but clean up explicitly too
	statements := []string{
		"DELETE FROM relations WHERE workspace_id = $1",
		"DELETE FROM knowledge_objects WHERE workspace_id = $1",
	}
	for _, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt, workspaceID); err != nil {
			t.Fatalf("cleanup relation workspace: %v", err)
		}
	}
}
