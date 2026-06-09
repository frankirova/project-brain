# Design: Knowledge Relations

## Technical Approach

Add a standalone `relations` table and repository layer for typed directed edges between knowledge objects. Follows the existing pgx transaction pattern from `repositories.go` but operates independently — no coupling to `IngestionUnitOfWork`. Domain types, repository interface, and PostgreSQL implementation extend the existing files with additive changes. Workspace isolation enforced via FK references (objects must belong to the same workspace for the FK to resolve, though we scope queries explicitly).

## Architecture Decisions

### Decision: Standalone Repository (No UoW Coupling)

**Choice**: Independent `RelationRepository` outside `IngestionUnitOfWork`
**Alternatives**: (a) Add to `IngestionRepositories` interface, (b) Create a separate `RelationUnitOfWork`
**Rationale**: Relations are consumed by agents, commands, and future ingestion hooks — not just ingestion. Coupling to ingestion UoW would force unnecessary transaction boundaries. Independent repo keeps the change additive with zero regression risk to ingestion.

### Decision: CHECK Constraint + Application Validation

**Choice**: Enforce `source_object_id != target_object_id` at both DB and Go layers
**Alternatives**: DB-only, or application-only
**Rationale**: Dual enforcement is the existing pattern (see `object_sources.relevance` CHECK). DB catches bypass; app avoids wasted round-trips and gives clean error messages.

### Decision: TEXT Type Column (Not ENUM)

**Choice**: `relation_type TEXT` with Go-layer validation against 14 constants
**Alternatives**: PostgreSQL ENUM type, lookup table
**Rationale**: Follows existing pattern — `sources.type`, `knowledge_objects.type`, `audit_events.action` are all TEXT. ENUM requires a migration for every type change. TEXT + Go constants is consistent with the codebase and flexible for future types.

### Decision: Unique Index on (workspace_id, source_object_id, target_object_id, relation_type)

**Choice**: Composite unique index including workspace_id
**Alternatives**: Unique on (source, target, type) without workspace
**Rationale**: Workspace scoping is mandatory. Two workspaces should be able to have the same (A→B, supports) pair independently. Index also serves the query-by-source and query-by-target paths.

## Data Flow

```
Caller (agent/command/future API)
  │
  ▼
RelationRepository.Create(relation)
  │
  ├─ Go validation: type in allowed set, source != target
  │
  ▼
PostgreSQL INSERT INTO relations
  │
  ├─ CHECK: source != target
  ├─ UNIQUE: (workspace_id, source, target, type)
  └─ FK: source_object_id → knowledge_objects(id) ON DELETE CASCADE
      FK: target_object_id → knowledge_objects(id) ON DELETE CASCADE
```

Query paths: `FindBySourceObjectID` → `WHERE workspace_id=$1 AND source_object_id=$2`
`FindByTargetObjectID` → `WHERE workspace_id=$1 AND target_object_id=$2`
`FindByType` → `WHERE workspace_id=$1 AND relation_type=$2`

## File Changes

| File | Action | Description |
|------|--------|-------------|
| `migrations/0002_relations.sql` | Create | CREATE TABLE relations + indexes |
| `internal/domain/knowledge.go` | Modify | Add `Relation` struct, `RelationType` constants, `RelationInput` |
| `internal/app/ports.go` | Modify | Add `RelationRepository` interface |
| `internal/postgres/repositories.go` | Modify | Add `relationRepository` + wire into `repositories` struct |

## Interfaces / Contracts

```go
// internal/domain/knowledge.go — additions

type RelationType string

const (
    RelationTypeRelatesTo     RelationType = "relates_to"
    RelationTypeDependsOn     RelationType = "depends_on"
    RelationTypeContradicts   RelationType = "contradicts"
    RelationTypeSupersedes    RelationType = "supersedes"
    RelationTypeSupports      RelationType = "supports"
    RelationTypeDerivedFrom   RelationType = "derived_from"
    RelationTypeMentions      RelationType = "mentions"
    RelationTypeDecides       RelationType = "decides"
    RelationTypeImplements    RelationType = "implements"
    RelationTypeComparesWith  RelationType = "compares_with"
    RelationTypeReplaces      RelationType = "replaces"
    RelationTypeBlocks        RelationType = "blocks"
    RelationTypeReferences    RelationType = "references"
    RelationTypePartOf        RelationType = "part_of"
)

type Relation struct {
    ID               uuid.UUID
    WorkspaceID      string
    SourceObjectID   uuid.UUID
    TargetObjectID   uuid.UUID
    RelationType     RelationType
    Confidence       *float64
    Evidence         string
    Metadata         Metadata
    CreatedAt        time.Time
}

type RelationInput struct {
    SourceObjectID uuid.UUID    `json:"source_object_id"`
    TargetObjectID uuid.UUID    `json:"target_object_id"`
    RelationType   RelationType `json:"relation_type"`
    Confidence     *float64     `json:"confidence"`
    Evidence       string       `json:"evidence"`
    Metadata       Metadata     `json:"metadata"`
}
```

```go
// internal/app/ports.go — addition

type RelationRepository interface {
    Create(ctx context.Context, relation domain.Relation) error
    FindBySourceObjectID(ctx context.Context, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error)
    FindByTargetObjectID(ctx context.Context, workspaceID string, objectID uuid.UUID) ([]domain.Relation, error)
    FindByType(ctx context.Context, workspaceID string, relType domain.RelationType) ([]domain.Relation, error)
}
```

## Testing Strategy

| Layer | What to Test | Approach |
|-------|-------------|----------|
| Unit | Go validation (type check, self-reference) | Table-driven tests on `RelationInput` |
| Integration | CRUD + queries + constraint violations | pgx test database, exercise repository methods |
| Integration | Cascade delete | Delete source/target object, verify relation removed |
| Integration | Workspace isolation | Cross-workspace query returns empty |

## Migration / Rollout

New migration `0002_relations.sql`. Additive only — no existing tables or data modified. Rollback: drop the migration.

## Open Questions

- [ ] Should `FindByType` return results ordered by `created_at DESC`? (Spec doesn't specify ordering)
- [ ] Should `RelationRepository` live in the same `repositories.go` file or a new `relations.go`?
- [ ] Confidence bounds: spec says NULL allowed, but should we CHECK `confidence >= 0 AND confidence <= 1` like `object_sources.relevance`?
