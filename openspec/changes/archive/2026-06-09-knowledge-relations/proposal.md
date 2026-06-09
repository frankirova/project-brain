# Proposal: Knowledge Relations

## Intent

The knowledge core stores entities and sources but has no way to express relationships between them. PROJECT_BRAIN.md section 10.2 defines 14 relation types (depends_on, contradicts, supersedes, etc.) that agents, commands, and future ingestion hooks need to link knowledge objects. Without relations, the knowledge graph is flat — objects exist in isolation.

## Scope

### In Scope

- `relations` table with CHECK constraint (`source != target`), unique pair index, and source/target/type indexes
- `Relation` domain type with 14 typed constants
- `RelationInput` creation struct
- `RelationRepository` interface: Create, FindBySourceObjectID, FindByTargetObjectID, FindByType
- `relationRepository` PostgreSQL implementation following existing patterns
- Migration `0002_relations.sql`

### Out of Scope

- HTTP/gRPC API endpoints (internal-only for now)
- Integration with ingestion flow (`IngestTextService` untouched)
- Relation creation by agents or commands (future consumers)
- Bidirectional query helpers or graph traversal

## Capabilities

### New Capabilities

- `knowledge-relations`: Standalone relation persistence — store and query typed directed edges between knowledge objects within a workspace

### Modified Capabilities

None — no existing spec-level behavior changes.

## Approach

Standalone relations (Approach 1 from exploration). Add a new `relations` table and repository outside the ingestion UoW. Existing ingestion flow is untouched. The repository uses the same pgx transaction pattern but operates independently — no `IngestionUnitOfWork` coupling. Later, when ingestion needs atomic object+relation creation, `RelationRepository` can be added to a new UoW or used in a composite service.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `migrations/0002_relations.sql` | New | CREATE TABLE + indexes for relations |
| `internal/domain/knowledge.go` | Modified | Add Relation struct, RelationType constants, RelationInput |
| `internal/app/ports.go` | Modified | Add RelationRepository interface |
| `internal/postgres/repositories.go` | Modified | Add relationRepository impl + add to repositories struct |
| `internal/app/ingest_text.go` | None | Unchanged |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Self-referencing relations | Low | CHECK constraint enforces `source_object_id != target_object_id` |
| Orphaned relations on object deletion | Low | FK CASCADE on both source and target FKs |
| Type validation gap (TEXT in DB) | Low | Go-layer validation of RelationType constants; acceptable for MVP |
| Bidirectional query performance | Low | Separate indexes on source_object_id and target_object_id |

## Rollback Plan

- Drop migration `0002_relations.sql` (table + indexes)
- Remove `Relation`, `RelationType`, `RelationInput` from `knowledge.go`
- Remove `RelationRepository` from `ports.go`
- Remove `relationRepository` from `repositories.go`
- No existing ingestion code is affected — rollback is additive deletion only

## Dependencies

- Existing `knowledge_objects` table (FK targets)
- Migration runner infrastructure

## Success Criteria

- [ ] Migration `0002_relations.sql` applies cleanly
- [ ] `RelationRepository.Create` persists a relation and rejects self-references
- [ ] `FindBySourceObjectID`, `FindByTargetObjectID`, `FindByType` return correct results
- [ ] Unique pair constraint prevents duplicate `(source, target, type)` combinations
- [ ] Existing ingestion flow unaffected (no regressions)
