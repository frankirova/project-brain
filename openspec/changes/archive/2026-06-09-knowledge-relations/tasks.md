# Tasks: Knowledge Relations

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | ~200–250 |
| 400-line budget risk | Low |
| Chained PRs recommended | No |
| Suggested split | single PR |
| Delivery strategy | single-pr |
| Chain strategy | pending |

Decision needed before apply: Yes
Chained PRs recommended: No
Chain strategy: pending
400-line budget risk: Low

### Suggested Work Units

| Unit | Goal | Likely PR | Notes |
|------|------|-----------|-------|
| 1 | Migration, domain types, repository interface, PostgreSQL impl, unit + integration tests | PR 1 | ~200-250 lines; includes all deliverables for standalone relations |

## Phase 1: Migration & Domain Types

- [x] 1.1 Create `migrations/0002_relations.sql` — `CREATE TABLE relations` with columns: id UUID PK, workspace_id TEXT NOT NULL, source_object_id UUID NOT NULL FK → knowledge_objects(id) ON DELETE CASCADE, target_object_id UUID NOT NULL FK → knowledge_objects(id) ON DELETE CASCADE, relation_type TEXT NOT NULL, confidence NUMERIC(4,3) NULL CHECK (confidence >= 0 AND confidence <= 1), evidence TEXT NULL, metadata JSONB NOT NULL DEFAULT '{}', created_at TIMESTAMPTZ NOT NULL DEFAULT now(). Add CHECK constraint `source_object_id != target_object_id`. Add composite unique index on (workspace_id, source_object_id, target_object_id, relation_type). Add index on (workspace_id, source_object_id) and (workspace_id, target_object_id) and (workspace_id, relation_type).
- [x] 1.2 Add to `internal/domain/knowledge.go`: `RelationType` type (string), 14 named constants (relates_to, depends_on, contradicts, supersedes, supports, derived_from, mentions, decides, implements, compares_with, replaces, blocks, references, part_of), `Relation` struct (ID, WorkspaceID, SourceObjectID, TargetObjectID, RelationType, Confidence *float64, Evidence, Metadata, CreatedAt), `RelationInput` struct with JSON tags, and a `ValidateRelationType(relType RelationType) bool` function.

## Phase 2: Repository Interface & Implementation

- [x] 2.1 Add `RelationRepository` interface to `internal/app/ports.go` with methods: Create(ctx, domain.Relation) error, FindBySourceObjectID(ctx, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error), FindByTargetObjectID(ctx, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error), FindByType(ctx, workspaceID string, relType domain.RelationType) ([]domain.Relation, error).
- [x] 2.2 Add `relationRepository` struct (pgx.Tx) to `internal/postgres/repositories.go`. Implement `Create`: validate RelationType via domain.ValidateRelationType, marshal metadata, INSERT with $1–$10 params. Implement `FindBySourceObjectID`: SELECT WHERE workspace_id=$1 AND source_object_id=$2, scan into []domain.Relation. Implement `FindByTargetObjectID`: SELECT WHERE workspace_id=$1 AND target_object_id=$2. Implement `FindByType`: SELECT WHERE workspace_id=$1 AND relation_type=$2. Use nullableString for nullable text fields, marshalMetadata for JSONB. Add interface assertion: `var _ app.RelationRepository = (*relationRepository)(nil)`.
- [x] 2.3 Wire `relationRepository` into the `repositories` struct and add a `Relations()` accessor. Note: `relationRepository` is standalone (not transaction-bound via IngestionUnitOfWork). It needs its own pgx connection — add a `conn *pgx.Conn` field to `relationRepository` instead of `tx pgx.Tx`, and accept the connection via a constructor or direct struct init.

## Phase 3: Unit & Integration Tests

- [x] 3.1 Create `internal/domain/knowledge_test.go` — table-driven tests for `ValidateRelationType`: valid types accepted, invalid type rejected. Test `RelationInput` struct with JSON unmarshalling.
- [x] 3.2 Create `internal/postgres/relation_repository_test.go` — integration tests using a pgx test database. Tests per spec scenario: successful creation, creation with minimal fields (NULL confidence/evidence), self-reference rejected (app-level), duplicate pair rejected, same pair different type allowed, find by source returns correct relations, find by source empty, find by target, find by type, workspace isolation for all queries, cascade delete when source deleted, cascade delete when target deleted, unrelated relations preserved after delete.
