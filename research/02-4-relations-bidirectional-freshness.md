# 02-4 — Relations Bidirectional & Freshness (change #12)

Closes HIGH risk §2.4 of the backlog. Captured 2026-06-10.

---

## TL;DR

Change #12 extends the `relations` table with three new columns
(`weight`, `valid_from`, `valid_until`) and adds a new
`GraphExpander` retriever. It's the smallest of the four §2 HIGH
risks (3 days) and **unblocks the formalization of `agreement_score`**
that closes the ADR-002 debt.

**Eight concrete decisions**:

1. **`weight NUMERIC(4,3) NOT NULL DEFAULT 1.0`** with `CHECK
   (weight >= 0 AND weight <= 1)`. Same range as `confidence`.
   The RelationBuilder can override; default is "all relations
   equally weighted".
2. **`valid_from TIMESTAMPTZ NULL`** and **`valid_until TIMESTAMPTZ
   NULL`**. Both nullable. NULL means "no temporal bound". A
   `CHECK (valid_until IS NULL OR valid_from IS NULL OR
   valid_until > valid_from)` keeps the range valid.
3. **Bidirectional traversal via query-time `UNION ALL`**, not
   inverse-edge duplication. Clean, no schema duplication.
   Sufficient for < 100k objects.
4. **`FindByType` orders by `weight DESC, created_at DESC`**.
   Recency is the tiebreaker.
5. **New `GraphExpander` retriever** in
   `internal/postgres/graph_expander.go`. SQL recursive CTE with
   `max_hops = 3` default, cycle detection, validity filter,
   weight threshold. Hydrates via the existing `FTSRetriever.FindByID`.
6. **Three new indexes** for the new query patterns: `(workspace_id,
   relation_type, weight DESC)`, `(source, valid_from, valid_until)
   WHERE valid_until IS NOT NULL`, `(target, valid_from,
   valid_until) WHERE valid_until IS NOT NULL`.
7. **Migration `0015_relations_freshness.sql`**: `ALTER TABLE`
   adds the columns (fast on PG 11+ for nullable / constant-
   default), `CREATE INDEX` adds the 3 new ones.
8. **`agreement_score` formalization is a follow-up, not part of
   this change.** The schema in this change supports it; the
   SQL view that closes the ADR-002 debt is a small separate
   PR.

---

## What change #12 needs to do

Per `tasks.md:101-106`, `proposal.md:444-450`, `design.md:498-506`:

1. Add 3 columns: `weight`, `valid_from`, `valid_until`
2. Add `FindByType` with `ORDER BY weight DESC` (the current
   `FindByType` does not order)
3. Add `GraphExpander` retriever
4. New migration `0015_relations_freshness.sql`
5. Wire the `GraphExpander` into the `CompositeRetriever` (change
   #10 territory, but the new retriever is delivered here)

---

## The exact gap (cite + line)

### Current schema (lacking the 3 columns)

`migrations/0002_relations.sql:1-13`:

```sql
CREATE TABLE IF NOT EXISTS relations (
    id UUID PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    source_object_id UUID NOT NULL REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    target_object_id UUID NOT NULL REFERENCES knowledge_objects(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL,
    confidence NUMERIC(4,3) CHECK (confidence >= 0 AND confidence <= 1),
    evidence TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (source_object_id != target_object_id),
    UNIQUE (workspace_id, source_object_id, target_object_id, relation_type)
);
```

No `weight`, no `valid_from`, no `valid_until`. Indexes don't
support `ORDER BY weight` queries. Bidirectional traversal isn't
formalized.

### Current `FindByType` (no order)

`internal/postgres/repositories.go:299-309`:

```go
func (r *relationRepository) FindByType(ctx context.Context, workspaceID string, relType domain.RelationType) ([]domain.Relation, error) {
    rows, err := r.conn.Query(ctx, `
SELECT id, workspace_id, source_object_id, target_object_id, relation_type, confidence, evidence, metadata, created_at
FROM relations
WHERE workspace_id = $1 AND relation_type = $2`, workspaceID, string(relType))
    ...
}
```

No `ORDER BY`. Results in undefined order. Cannot rank by
"strength" because the column doesn't exist.

### Current `Relation` struct (lacking the fields)

`internal/domain/knowledge.go:163-174`:

```go
type Relation struct {
    ID             uuid.UUID
    WorkspaceID    string
    SourceObjectID uuid.UUID
    TargetObjectID uuid.UUID
    RelationType   RelationType
    Confidence     *float64
    Evidence       string
    Metadata       Metadata
    CreatedAt      time.Time
}
```

No `Weight`, no `ValidFrom`, no `ValidUntil`.

### The recursive CTE (already designed, not yet implemented)

`design.md:471-486`:

```sql
WITH RECURSIVE graph(object_id, depth, path) AS (
    SELECT $seed, 0, ARRAY[$seed]
    UNION ALL
    SELECT r.target, g.depth + 1, g.path || r.target
    FROM graph g
    JOIN relations r ON r.source = g.object_id
    WHERE g.depth < $max_hops
      AND NOT (r.target = ANY(g.path))  -- cycle detection
)
SELECT object_id, depth FROM graph;
```

This is the design. The implementation does not exist yet (no
`GraphExpander` in the codebase).

### The debt it closes

`design.md:662-676`:

> "`agreement_score` lacks a formal DB-derivable definition.
> Candidate derivation: `agreement_score = 1 -
> (contradicting_relations / total_relations)` when
> `total_relations > 0`, else `1.0`. Formalizing this derivation
> is a small follow-up to the `relations-bidirectional-and-freshness`
> chained change (#12)."

The schema in #12 supports this follow-up: once we have a
DB-derivable `agreement_score`, the Compiler stops receiving it
externally (the ADR-002 debt closes).

---

## Sub-decisions inside change #12

### Decision 1: `weight` semantics

| Option | Type | Pros | Cons |
|---|---|---|---|
| **A. `NUMERIC(4,3) NOT NULL DEFAULT 1.0` with `CHECK (weight >= 0 AND weight <= 1)`** | Float, normalized | Same range as `confidence`; can be tuned by RelationBuilder | Slightly heavier than INT |
| B. `INTEGER` rank (1-100) | Simpler sort, faster | Less expressive; can't represent fractional weights | — |
| C. `FLOAT` unbounded | Most flexible | CHECK constraint needed; harder to reason about | — |

→ **A**. The normalized float range matches `confidence`, lets
the RelationBuilder express "this relation is 80% as strong as
the default", and the CHECK constraint keeps it sane.

### Decision 2: Temporal validity

| Field | Type | NULL means |
|---|---|---|
| `valid_from` | `TIMESTAMPTZ NULL` | "Always was valid" |
| `valid_until` | `TIMESTAMPTZ NULL` | "Still valid" |

CHECK constraint:

```sql
CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from)
```

The current edge is "valid" if `now() >= COALESCE(valid_from,
'-infinity') AND now() < COALESCE(valid_until, 'infinity')`.

### Decision 3: Bidirectional traversal

| Option | Pros | Cons |
|---|---|---|
| **A. Query-time `UNION ALL` (recommended)** | Clean, no schema duplication, works today | Each query has 2× the work; no inverse-edge index |
| B. Inverse-edge duplication in schema | Single-direction queries; faster reads | 2× storage; 2× write cost; consistency burden |
| C. `JOIN ON r.source = g.id OR r.target = g.id` (single query, both sides) | Single CTE | Confusing semantics; harder to maintain |

→ **A** for < 100k objects (current scale). The CTE walks
`source → target`. To walk `target → source` (inverse), the
recursion does a second pass with `r.source = g.object_id` and
the seed's inverse relations pre-fetched.

Implementation sketch:

```sql
WITH RECURSIVE
seeds AS (
    -- Forward: edges from seed
    SELECT target_object_id AS next_id, 1 AS depth, ARRAY[source_object_id] AS path, weight
    FROM relations
    WHERE source_object_id = $1
      AND workspace_id = $2
      AND $now >= COALESCE(valid_from, '-infinity')
      AND $now < COALESCE(valid_until, 'infinity')
    UNION ALL
    -- Inverse: edges to seed
    SELECT source_object_id AS next_id, 1 AS depth, ARRAY[target_object_id] AS path, weight
    FROM relations
    WHERE target_object_id = $1
      AND workspace_id = $2
      AND $now >= COALESCE(valid_from, '-infinity')
      AND $now < COALESCE(valid_until, 'infinity')
),
graph(next_id, depth, path) AS (
    SELECT next_id, depth, path FROM seeds WHERE NOT (next_id = ANY(path))
    UNION ALL
    SELECT r.target_object_id, g.depth + 1, g.path || r.target_object_id
    FROM graph g
    JOIN relations r ON r.source_object_id = g.next_id
    WHERE g.depth < $max_hops
      AND workspace_id = $2
      AND NOT (r.target_object_id = ANY(g.path))
      AND $now >= COALESCE(r.valid_from, '-infinity')
      AND $now < COALESCE(r.valid_until, 'infinity')
)
SELECT next_id, MIN(depth) AS depth, MAX(weight) AS weight
FROM graph
GROUP BY next_id
ORDER BY depth, weight DESC
LIMIT $limit;
```

This is one query. Works for both directions. The cycle
detection is the `NOT (r.target = ANY(g.path))` pattern.

### Decision 4: `FindByType` ordering

New SQL:

```sql
SELECT ...
FROM relations
WHERE workspace_id = $1 AND relation_type = $2
ORDER BY weight DESC, created_at DESC
```

The `weight DESC` is primary; `created_at DESC` is the tiebreaker
so newer relations win when weights are equal.

This requires the new index:
```sql
CREATE INDEX idx_relations_workspace_type_weight
    ON relations (workspace_id, relation_type, weight DESC);
```

### Decision 5: `GraphExpander` retriever

New file: `internal/postgres/graph_expander.go`.

```go
package postgres

import (
    "context"
    "fmt"
    "time"

    "github.com/frankirova/project-brain/internal/app"
    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgxpool"
)

// graphExpander is a Retriever that walks the relations graph
// from a seed object, applying weight and validity filters.
type graphExpander struct {
    pool      *pgxpool.Pool
    objects   *FTSRetriever
    maxHops   int
    minWeight float64
}

func NewGraphExpander(pool *pgxpool.Pool, objects *FTSRetriever, maxHops int, minWeight float64) *graphExpander {
    if maxHops <= 0 {
        maxHops = DefaultMaxHops
    }
    return &graphExpander{pool: pool, objects: objects, maxHops: maxHops, minWeight: minWeight}
}

func (g *graphExpander) Search(ctx context.Context, q app.SearchQuery) ([]app.SearchResult, error) {
    if q.SeedObjectID == nil {
        return nil, nil
    }
    // 1. Run the recursive CTE to get the related object IDs + depth + weight.
    rows, err := g.pool.Query(ctx, /* the CTE above with q.SeedObjectID, q.WorkspaceID, maxHops, minWeight, limit */)
    if err != nil { return nil, err }
    defer rows.Close()
    
    // 2. Hydrate each via FTSRetriever.FindByID (same pattern as vectorRetriever).
    var results []app.SearchResult
    for rows.Next() {
        var id uuid.UUID
        var depth int
        var weight float64
        if err := rows.Scan(&id, &depth, &weight); err != nil { return nil, err }
        obj, err := g.objects.FindByID(ctx, q.WorkspaceID, id)
        if err != nil { continue /* race: object deleted between scan and hydrate */ }
        // 3. Score: closer + heavier = higher. Score = weight / (1 + depth).
        score := weight / float64(1+depth)
        results = append(results, app.SearchResult{
            Object: *obj,
            Score:  score,
            MatchType: "graph",
            Depth:  depth,
        })
    }
    return results, rows.Err()
}
```

**Score formula**: `score = weight / (1 + depth)`. A weight-1.0
edge at depth 1 scores 0.5. A weight-0.5 edge at depth 2 scores
0.167. RRF will fuse this with the other retrievers.

The `SearchQuery` struct needs a new optional field:
`SeedObjectID *uuid.UUID`. When set, the graph expander runs;
when nil, it returns nil (graph expansion needs a seed).

### Decision 6: Indexes

Three new indexes (migration `0015`):

```sql
-- For FindByType with weight ordering.
CREATE INDEX IF NOT EXISTS idx_relations_workspace_type_weight
    ON relations (workspace_id, relation_type, weight DESC);

-- For the recursive CTE, forward direction, validity-aware.
CREATE INDEX IF NOT EXISTS idx_relations_source_validity
    ON relations (source_object_id, valid_from, valid_until)
    WHERE valid_until IS NOT NULL;

-- For the recursive CTE, inverse direction, validity-aware.
CREATE INDEX IF NOT EXISTS idx_relations_target_validity
    ON relations (target_object_id, valid_from, valid_until)
    WHERE valid_until IS NOT NULL;
```

The partial indexes (`WHERE valid_until IS NOT NULL`) keep them
small: most relations are timeless (NULL `valid_until`), so the
partial index only covers the small set with bounded validity.

### Decision 7: Migration cost

`ALTER TABLE relations ADD COLUMN` is fast on PG 11+ for:

- Nullable columns (`valid_from`, `valid_until`): no rewrite.
- Constant-default columns (`weight DEFAULT 1.0` with NOT NULL):
  PG 11+ stores the default in the catalog and lazily backfills
  on read. **No full table rewrite.**

For a 100k-row `relations` table, this migration runs in
**milliseconds**, not minutes. No need for `CONCURRENTLY` on the
column add (but it's still safe).

The 3 new indexes DO benefit from `CONCURRENTLY` to avoid
blocking writes during the build:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_relations_workspace_type_weight
    ON relations (workspace_id, relation_type, weight DESC);
```

### Decision 8: `agreement_score` follow-up

The follow-up is small (~½ day):

1. New SQL view `v_object_agreement_score`:
   ```sql
   CREATE OR REPLACE VIEW v_object_agreement_score AS
   SELECT
       object_id,
       workspace_id,
       CASE WHEN total_relations = 0 THEN 1.0
            ELSE 1.0 - (contradicting_relations::numeric / total_relations::numeric)
       END AS agreement_score
   FROM (
       SELECT
           o.id AS object_id,
           o.workspace_id,
           COUNT(r.id) AS total_relations,
           COUNT(r.id) FILTER (WHERE r.relation_type = 'contradicts') AS contradicting_relations
       FROM knowledge_objects o
       LEFT JOIN relations r
           ON r.workspace_id = o.workspace_id
           AND (r.source_object_id = o.id OR r.target_object_id = o.id)
           AND (r.valid_from IS NULL OR r.valid_from <= now())
           AND (r.valid_until IS NULL OR now() < r.valid_until)
       GROUP BY o.id, o.workspace_id
   ) counts;
   ```

2. Update the Compiler to read `agreement_score` from the view
   instead of receiving it externally from the RelationBuilder.

3. Update ADR-002 status from "Accepted with documented debt" to
   "Accepted; debt paid".

This is **not** part of change #12. It's the "small follow-up
after change #12 lands" mentioned in `design.md:784-788`.

---

## What goes in change #12

1. **New migration `migrations/0015_relations_freshness.sql`**
   with the column adds + new indexes (`CONCURRENTLY` for the
   indexes).
2. **Update `domain.Relation` and `domain.RelationInput`** in
   `internal/domain/knowledge.go` with the 3 new fields.
3. **Update `relationRepository.Create`** to write the new
   fields.
4. **Update `relationRepository.FindByType`** to `ORDER BY
   weight DESC, created_at DESC`.
5. **Add `graphExpander`** in
   `internal/postgres/graph_expander.go`.
6. **Update `app.SearchQuery`** to add `SeedObjectID *uuid.UUID`.
7. **Update `app.Retriever` port** (no signature change; just
   document the seed).
8. **Wire the `graphExpander` into `CompositeRetriever`** in
   `internal/app/composite_retriever.go` (4th leg of RRF).
9. **Update `TestRelationRepositoryFindByType`** to assert the
   new ordering.
10. **Add tests**:
    - `TestGraphExpanderFromSeed` — depth, cycle detection
    - `TestGraphExpanderSkipsExpiredEdges` — `valid_until < now()`
    - `TestGraphExpanderSkipsLowWeightEdges` — `weight < minWeight`
    - `TestGraphExpanderBidirectional` — inverse edges work
    - `TestGraphExpanderHydrates` — race-safe hydration
11. **Update `relation_repository_test.go`** to set the new
    fields (or accept the defaults).
12. **Update `design.md`** to remove the "lacks weight, valid_from,
    valid_until" entry from §9.
13. **Update ADR-002** to note that the follow-up can now be
    specified (not in this change; just status).

---

## What this is NOT

- **Not a graph DB**. We stay in PostgreSQL with a CTE. At > 100k
  objects, a closure table is a separate change (`object_closure`).
- **Not a knowledge graph refactor**. The 14 relation types stay
  the same.
- **Not the `agreement_score` formalization**. That's a follow-up
  using the new schema.
- **Not a wire into `cmd/api/main.go`**. The composite retriever
  wiring is change #10. The new `graphExpander` integrates with
  it; the wiring is a separate (small) task.
- **Not a multi-tenant change**. The schema is already multi-
  tenant via `workspace_id`.

---

## Risks and edge cases

### Risk 1: New columns on a hot table

`relations` is heavily used (every knowledge graph query hits it).
Adding 3 columns is fast on PG 11+ (no rewrite), but the new
indexes will take time to build on a populated table.

**Mitigation**: `CONCURRENTLY` for the 3 new indexes. The column
adds are instant; the indexes are the cost.

### Risk 2: Existing `FindByType` callers

The current `FindByType` returns in undefined order. Code that
uses it may not care about order. The new `ORDER BY` adds cost
(sort or index scan). For a 100k-row table, the new index makes
this O(log N) per type.

**Mitigation**: the new index covers the new query pattern. The
old query still works (the planner can ignore the new index and
do a sort if it wants).

### Risk 3: CTE performance on large graphs

The recursive CTE is O(E) per hop, where E is the number of
edges. At 100k objects × 5 edges each = 500k edges, 3 hops is
manageable. At 1M objects × 5 = 5M edges, 3 hops could be slow.

**Mitigation**: the design already flags this (`design.md:493-501`).
At > 100k objects, a closure table is the next step. For now,
the CTE is fine.

### Risk 4: `weight` as a ranking signal

If `weight` defaults to 1.0 for all relations, the ranking
becomes a recency order (since `created_at DESC` is the
tiebreaker). That's fine for the MVP. The RelationBuilder can
override weights later.

**Mitigation**: the `weight` default of 1.0 is a safe baseline.
The CHECK constraint keeps weights sane. No regression risk.

### Risk 5: Bidirectional CTE complexity

The `UNION ALL` over both directions is a more complex query
than the unidirectional one. Harder to debug, harder to optimize.

**Mitigation**: well-tested in the integration test
(`TestGraphExpanderBidirectional`). The query plan can be
inspected with `EXPLAIN ANALYZE`.

### Risk 6: Hydration races

The CTE returns object IDs; the hydration step (`FindByID`) runs
after. If an object is deleted between the CTE and the hydration,
the hydration returns `pgx.ErrNoRows` and we skip (same pattern
as `vectorRetriever`).

**Mitigation**: the same race-safety pattern as the vector
retriever. Tested in `TestGraphExpanderHydrates`.

### Risk 7: Partial index with `WHERE valid_until IS NOT NULL`

The partial index is small (only the relations with bounded
validity). But if most relations are timeless (NULL
`valid_until`), the index might be too small to be useful.

**Mitigation**: this is the right index for the query pattern
("edges that have a validity check"). For timeless edges, the
existing `(workspace_id, source/target)` indexes are used.

---

## Connection to other changes

| Change | Dependency on #12 |
|---|---|
| #1 workspace-id-uuid-migration | None. Independent. |
| #2 lifecycle-states-migration | None. Independent. |
| #3 versioning-and-versions-table | None. Independent. |
| #4 freshness-and-owner | None. Independent. |
| #5 raw-inputs-inbox | None. Independent. |
| #6 lifecycle-events-log | None. Independent. |
| #7 knowledge-compiler | Indirect: after #12, `agreement_score` is derivable. The Compiler can use the new derivation. |
| #8 event-bus-nats | None. Independent. |
| #9 agents-shared-brain | None. Independent. |
| #10 hybrid-retrieval-wiring | **Direct**: the `GraphExpander` delivered here is the 4th leg of the composite retriever wired in #10. |
| #11 hnsw-embedding-index | None. Independent. |
| #13 audit-duplicate-write | None. Independent. |
| #14 telegram-validation-ui | None. Independent. |
| #15 rodoc-paradigm-update | None. Doc only. |

After #12 lands:
- The `agreement_score` formalization (closing ADR-002 debt) is
  a small follow-up (~½ day, separate change).
- The closure table for > 100k objects is a future change
  (`object_closure`).

---

## What this DOES enable

- **`GraphExpander` is the 4th leg of the hybrid retriever**.
  Wired in change #10, it gives RRF a structured way to find
  related objects.
- **Freshness-aware edges**. The `valid_from` / `valid_until`
  columns let the system know "this relation was true from 2020
  to 2024" — useful for "what decisions are still valid?".
- **Weighted relations**. The `weight` column lets the
  RelationBuilder express "this `supports` is strong, this
  `mentions` is weak". FindByType now returns them in order.
- **Bidirectional traversal**. Inverse edges work without
  schema duplication. "What objects are related to X" is now a
  symmetric query.
- **`agreement_score` follow-up**. The schema supports it; the
  next change closes the ADR-002 debt.

---

## Spec anchors

- `migrations/0002_relations.sql:1-13` — current schema
- `internal/domain/knowledge.go:163-184` — Relation/RelationInput structs
- `internal/postgres/repositories.go:299-309` — current FindByType
- `internal/postgres/relation_repository_test.go:265-358` — tests
- `openspec/changes/paradigm-knowledge-os/tasks.md:101-106` — change #12
- `openspec/changes/paradigm-knowledge-os/proposal.md:444-450` — proposal
- `openspec/changes/paradigm-knowledge-os/design.md:471-506` — graph expansion design
- `openspec/changes/paradigm-knowledge-os/design.md:662-683` — `agreement_score` debt + relation columns debt
- `openspec/changes/paradigm-knowledge-os/exploration.md:546-550` — exploration
