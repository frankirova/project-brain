# Exploration: Knowledge Relations

## Current State

The knowledge core has 4 tables and 4 domain types:
- **sources** — origin of knowledge (text, PDF, URL, etc.)
- **knowledge_objects** — typed knowledge entities (document, research, decision, etc.)
- **object_sources** — many-to-many link between objects and sources with relevance score
- **audit_events** — immutable audit trail of all mutations

The domain model (`internal/domain/knowledge.go`) mirrors this 1:1. The application layer uses a **Unit-of-Work pattern** with transaction-scoped repositories (`IngestionUnitOfWork` → `IngestionRepositories`). Each repository interface lives in `internal/app/ports.go`. PostgreSQL implementations live in `internal/postgres/repositories.go`.

**There is NO relation concept today.** The ingestion flow creates Source + KnowledgeObject + ObjectSource + AuditEvent in a single transaction. PROJECT_BRAIN.md section 10.2 defines the target `relations` table with 14 relation types.

## Affected Areas

- `migrations/0002_relations.sql` — NEW: relations table + indexes
- `internal/domain/knowledge.go` — NEW: Relation type, RelationType constants, RelationInput
- `internal/app/ports.go` — NEW: RelationRepository interface
- `internal/postgres/repositories.go` — NEW: relationRepository implementation + add to repositories struct
- `internal/app/ingest_text.go` — NOT TOUCHED (relations are a separate concern for now)

## Approaches

| Approach | Pros | Cons | Effort |
|----------|------|------|--------|
| **1. Standalone relations** | Small scope, no risk to existing ingestion, clean separation | No auto-relations during ingestion | Low |
| **2. Ingestion-integrated** | Atomic object + relation creation | Changes ingestion contract, couples two concerns | Medium |
| **3. Relation service + hooks** | Decoupled, extensible | Two transactions, not atomic | Low-Medium |

## Recommendation

**Approach 1 — Standalone relations.** The ingestion flow is stable and tested. Relations are an independent concern — created by agents, API calls, or future hooks. The UoW pattern makes it easy to add `Relations()` accessor later if transactional ingestion+relation is needed.

## Proposed Schema

```sql
CREATE TABLE relations (
    id UUID PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    source_object_id UUID NOT NULL REFERENCES knowledge_objects(id),
    target_object_id UUID NOT NULL REFERENCES knowledge_objects(id),
    relation_type TEXT NOT NULL,
    confidence NUMERIC(4,3),
    evidence TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata JSONB NOT NULL DEFAULT '{}',
    CHECK (source_object_id != target_object_id)
);

CREATE UNIQUE INDEX idx_relations_unique_pair
  ON relations(source_object_id, target_object_id, relation_type);
CREATE INDEX idx_relations_source ON relations(source_object_id);
CREATE INDEX idx_relations_target ON relations(target_object_id);
CREATE INDEX idx_relations_type ON relations(relation_type);
```

**14 relation types**: relates_to, depends_on, contradicts, supersedes, supports, derived_from, mentions, decides, implements, compares_with, replaces, blocks, references, part_of.

## Domain Model

```go
type Relation struct {
    ID, WorkspaceID, SourceObjectID, TargetObjectID uuid.UUID
    RelationType string
    Confidence   float64
    Evidence     string
    CreatedAt    time.Time
    Metadata     Metadata
}

type RelationInput struct { /* JSON-tagged fields for creation */ }
```

## Repository Interface

```go
type RelationRepository interface {
    Create(ctx context.Context, relation domain.Relation) error
    FindBySourceObjectID(ctx, workspaceID, objectID) ([]Relation, error)
    FindByTargetObjectID(ctx, workspaceID, objectID) ([]Relation, error)
    FindByType(ctx, workspaceID, relationType) ([]Relation, error)
}
```

## API Surface

**Internal-only for now.** No HTTP/gRPC endpoints. Relations created by future Knowledge Processor Agent, `/research`, `/architect`, `/decision` commands, or direct service calls.

## Risks

- **Self-referencing**: CHECK constraint prevents `source = target`
- **Orphaned relations**: CASCADE on FK already handles deletion
- **Type validation**: TEXT in DB, validated in Go layer — acceptable for MVP
- **Bidirectional queries**: need both source and target indexes (covered)

## Ready for Proposal

**Yes.** Scope is clear, schema defined, domain model designed, integration point understood (deferred). Clean, low-risk addition to the knowledge core.
