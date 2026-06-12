# 04-11 — HNSW Embedding Index (quick win #11)

Closes quick-win #11 of the backlog. Captured 2026-06-10.

---

## TL;DR

The HNSW index on `embeddings.embedding` is **missing** today.
The migration comment in `migrations/0007_embeddings.sql:7-9`
*claims* it exists, but `migrations/0007_embeddings.sql:23-24`
only creates a B-tree on `workspace_id`. The query in
`internal/postgres/embeddings.go:69-76` does
`ORDER BY e.embedding <=> $1` against the raw table → **sequential
scan + sort** at runtime. Fine for tens of rows. Catastrophic at
thousands.

**Add the missing index** as a new migration `0014_hnsw_index.sql`:

```sql
-- 0014_hnsw_index.sql
-- Migration 0014: HNSW index on embeddings.embedding for cosine
-- similarity search. Fixes the gap in 0007_embeddings.sql:7-9
-- (the comment claimed HNSW existed; only the B-tree on
-- workspace_id was actually created).

-- Must run outside a transaction (CONCURRENTLY is incompatible
-- with transaction blocks). See the migration runner notes.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_embeddings_embedding_hnsw
    ON embeddings
    USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
```

Five things to know:

1. **Operator class is `vector_cosine_ops`** — matches the `<=>`
   operator used in `FindSimilar` (`internal/postgres/embeddings.go:73`).
2. **`CONCURRENTLY` is mandatory** — without it, the build takes
   an `ACCESS EXCLUSIVE` lock that blocks writes for the duration
   of the build. On a 100k-row table that's minutes; on 1M rows
   that's an hour of downtime.
3. **`m = 16, ef_construction = 64`** are the pgvector defaults.
   Sensible for MVP. Tune later if recall is poor.
4. **The migration runner must run this outside a transaction**.
   `CREATE INDEX CONCURRENTLY` cannot be in a transaction block.
   Document this in the migration tool's runner.
5. **Verification step**: after the migration runs, `EXPLAIN
   ANALYZE` the `FindSimilar` query and confirm the plan switches
   from `Seq Scan` to `Index Scan using idx_embeddings_embedding_hnsw`.

---

## The exact gap (cite + line)

**Comment in `0007_embeddings.sql:7-9`**:

> "The hnsw index uses cosine distance and gives sub-millisecond
> recall at MVP volumes."

**What `0007_embeddings.sql:23-24` actually creates**:

```sql
CREATE INDEX IF NOT EXISTS idx_embeddings_workspace_id
  ON embeddings (workspace_id);
```

Just a B-tree on `workspace_id`. The HNSW index is **not
created**. This is a documentation-vs-implementation drift
flagged in:

- `exploration.md:160-166, 436-445` — gap analysis
- `design.md:273-275, 519-522, 685-692` — risk + chained change
- `proposal.md:371, 379` — open risks
- `tasks.md:88-99` — change #11 description

**The query that suffers** (`internal/postgres/embeddings.go:69-76`):

```sql
SELECT e.object_id, 1 - (e.embedding <=> $1) AS score
FROM embeddings e
WHERE e.workspace_id = $2
ORDER BY e.embedding <=> $1
LIMIT $3
```

The `ORDER BY embedding <=> $1` has no index to back it. Postgres
falls back to a `Sort` node on top of a `Seq Scan`. The cost is
O(N log N) per query. At 10k rows × 1536 dims, that's a lot of
CPU and a slow query.

---

## Sub-decisions inside the change

### Operator class

| Class | Distance | Used by |
|---|---|---|
| `vector_l2_ops` | L2 (Euclidean) | `<->` |
| `vector_ip_ops` | Inner product | `<#>` (normalized vectors only) |
| `vector_cosine_ops` | Cosine | `<=>` ← **our operator** |
| `vector_l1_ops` | L1 (Manhattan) | `<+>` |

**Decision**: `vector_cosine_ops`. Matches the `<=>` operator in
the query. No code change needed.

### m parameter (graph out-degree)

| Value | Build time | Recall | Index size |
|---|---|---|---|
| 8 | Faster | Lower (~90%) | Smaller |
| **16 (pgvector default)** | Balanced | ~95% | Balanced |
| 24 | Slower | Higher (~98%) | Larger |
| 32+ | Much slower | Marginal gain | Much larger |

**Decision**: `m = 16`. The pgvector default. Sensible for MVP.
Recall at 16 is enough for a hybrid retriever where RRF will
re-rank anyway.

### ef_construction (build-time search width)

| Value | Build time | Recall |
|---|---|---|
| 32 | Faster | Lower |
| **64 (pgvector default)** | Balanced | ~95% |
| 128 | Slower | Higher |
| 200+ | Much slower | Marginal |

**Decision**: `ef_construction = 64`. The pgvector default. Only
raise it if the post-migration recall benchmark shows the index
is dropping results the old seq scan would have caught.

### ef_search (query-time search width)

This is a **per-query** parameter, not a build parameter. The
default is 40. The query can override it with:

```sql
SET LOCAL hnsw.ef_search = 100;
SELECT ... FROM embeddings ORDER BY embedding <=> $1 LIMIT 10;
```

**Decision**: leave the default (40) for MVP. If a query needs
higher recall, set `SET LOCAL hnsw.ef_search = N` in the
repository method.

### Build mode: CONCURRENTLY vs not

| Mode | Lock | Build time | Effect on writes |
|---|---|---|---|
| `CREATE INDEX ...` | `ACCESS EXCLUSIVE` briefly, then `SHARE` for the build | Normal | **Writes blocked** for the duration |
| **`CREATE INDEX CONCURRENTLY ...`** | None that blocks writes | 2-3x longer | **Writes never blocked** |

**Decision**: `CONCURRENTLY`. Mandatory for production. Cost is
build time, which is acceptable for a ½ día change.

**Gotcha**: `CONCURRENTLY` cannot be in a transaction block. The
migration runner must support this. Most Go migration tools
(`golang-migrate`, `goose`) do, but you have to opt in:

- `golang-migrate`: the driver handles it; just write the SQL.
- `goose`: needs `--allow-no-transaction` semantics; check the
  runner config.
- **Custom runner**: needs explicit `tx == nil` branch.

### Half-precision (`halfvec`) — NOT for this change

`halfvec(1536)` is half precision (2 bytes per dim vs 4). Half
the storage, half the index size, slight recall loss. The
upgrade path is documented for the future but **out of scope**
for #11. Switching requires a column type change and either
re-embedding all rows or a parallel column. Not a quick win.

### Idempotency: `IF NOT EXISTS`

`CREATE INDEX CONCURRENTLY IF NOT EXISTS` is supported. It lets
the migration re-run safely. **Gotcha**: if a previous build
failed and left an `INVALID` index, `IF NOT EXISTS` will skip it
and the new index won't be created. The migration looks
"successful" but the index is broken.

**Mitigation**: in the migration's "down" (if you have one) or
in a separate cleanup migration, add:

```sql
DROP INDEX CONCURRENTLY IF EXISTS idx_embeddings_embedding_hnsw;
```

before the `CREATE`. The runner executes them in order. This
ensures a fresh build every time.

---

## What goes in change #11

1. **New migration `0014_hnsw_index.sql`** with the SQL above.
2. **Update the migration tool's runner config** if needed (for
   `CONCURRENTLY` outside a transaction).
3. **Verification step** (post-migration): run `EXPLAIN ANALYZE`
   on the `FindSimilar` query. Expect `Index Scan using
   idx_embeddings_embedding_hnsw`.
4. **No code change** in `internal/postgres/embeddings.go`. The
   query is already correct; it just needs the index.
5. **Recall benchmark** (nice-to-have): before/after top-k
   comparison on a small fixture to confirm HNSW doesn't drop
   results the old seq scan returned.
6. **Update `migrations/0007_embeddings.sql:7-9` comment** to
   point to the new migration:

   ```sql
   -- HNSW index is added in migration 0014_hnsw_index.sql
   -- (CONCURRENTLY because the table may be live).
   ```

7. **Update the design doc** to remove the "HNSW gap" risk
   (this closes it).
8. **Update the backlog** to mark #11 done.

---

## Risks and edge cases

### Build time on large tables

HNSW build is O(N × m × ef_construction). At 1M rows × 1536 dims
× m=16 × ef_construction=64, expect a few minutes on a decent
machine. Monitor with `pg_stat_progress_create_index`:

```sql
SELECT pid, datname, relid::regclass, index_relid::regclass,
       command, phase, tuples_done, tuples_total
FROM pg_stat_progress_create_index;
```

### Failed builds leave `INVALID` indexes

If a `CONCURRENTLY` build is interrupted (connection drops, OOM,
server restart), the index is left in `INVALID` state. It
occupies disk but isn't used by the planner. The fix:

```sql
DROP INDEX CONCURRENTLY IF EXISTS idx_embeddings_embedding_hnsw;
-- then re-run the migration
```

Add a check in the migration:

```sql
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = 'idx_embeddings_embedding_hnsw'::regclass
          AND NOT indisvalid
    ) THEN
        RAISE NOTICE 'Dropping invalid index idx_embeddings_embedding_hnsw';
        DROP INDEX CONCURRENTLY IF EXISTS idx_embeddings_embedding_hnsw;
    END IF;
END $$;
```

### Memory and `maintenance_work_mem`

HNSW build needs significant memory. Tune before running:

```sql
SET maintenance_work_mem = '2GB';
```

This is a session-level setting; the migration runner should
apply it before the `CREATE INDEX`.

### Disk space

HNSW index size is ~2x the raw vector data. For 1M rows × 1536
dims × 4 bytes = 6 GB vectors, the index is ~12 GB. Make sure
the disk has headroom.

### Write amplification

HNSW updates are expensive. Every UPSERT to `embeddings` causes
graph rewrites in the index. At high write rates (thousands of
embeddings/sec), the index becomes a bottleneck. For MVP this
isn't an issue, but it's the natural next bottleneck to hit
when the corpus grows.

### Per-workspace HNSW partitioning — out of scope

If a single workspace has 10M+ embeddings, the per-workspace
HNSW index would be huge. Postgres can do partial indexes
(`WHERE workspace_id = 'specific-id'`) but it's hard to manage
per-workspace dynamically. Future change: partition
`embeddings` by `workspace_id` and rebuild HNSW per partition.

---

## What this is NOT

- **Not a code change to `FindSimilar`**. The query is correct.
  It just lacks the index.
- **Not a model change**. We're using the same 1536-dim
  embeddings.
- **Not a recall improvement**. The seq scan returned 100% recall
  (it scanned everything). HNSW is approximate; recall is
  typically 95-99%. This is acceptable for a hybrid retriever
  that re-ranks with RRF.
- **Not a halfvec upgrade**. That's a future change.
- **Not a fix for high write rates**. UPSERT-heavy workloads
  need different strategies (see "Write amplification" above).

---

## What this DOES enable

- **Hybrid retrieval at scale** (change #10). The
  `CompositeRetriever` already does RRF across FTS + vector +
  structured + graph. Without HNSW, the vector leg is a seq
  scan and the RRF fusion is bottlenecked.
- **Acceptable p99 latency** for vector search. Target: < 50ms
  p99 at 100k vectors, < 200ms p99 at 1M vectors. The HNSW
  index makes this achievable.
- **The "Fase 2 embedding" mark on ROADMAP.md**. Closing this
  gap moves the project from "vector search wired but
  unindexed" to "vector search production-ready".

---

## Spec anchors

- `migrations/0007_embeddings.sql:7-9, 23-24` — the gap
- `internal/postgres/embeddings.go:69-76` — the affected query
- `internal/domain/embedding.go` — the domain type
- `proposal.md:64-65, 325-328, 371, 379, 439-442` — gap
  analysis
- `design.md:269-275, 515-522, 598-610, 685-692` — risk +
  HNSW fix specification
- `exploration.md:13, 41-43, 160-166, 436-445` — exploration
  findings
- `tasks.md:88-99, 193` — change #11 description
