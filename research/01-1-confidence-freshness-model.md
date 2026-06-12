# 01-1 — Knowledge Confidence & Freshness Model

Captured from working session 2026-06-10. Grounded in
`openspec/changes/paradigm-knowledge-os/specs/knowledge-core-domain/spec.md`
and `openspec/changes/paradigm-knowledge-os/design.md`.

---

## TL;DR

Every `knowledge_object` exposes three confidence-shaped signals:

| Signal                | Stored?                       | Producer                            | Meaning                                                                 |
|-----------------------|-------------------------------|-------------------------------------|-------------------------------------------------------------------------|
| `confidence`          | Yes (nullable `NUMERIC(4,3)`) | The producer (agent / human / src)  | Self-reported trust in `[0, 1]`                                         |
| `computed_confidence` | No — derived on every read     | The system, via a pure formula      | Trust after considering evidence (sources, recency, lifecycle, etc.)    |
| `freshness`           | No — derived on every read     | The system, via a pure function     | Time-decay signal based on `next_review_at`                             |

The two derived values are **never persisted**. They are functions of inputs
that already live in the database. This is an explicit architectural
principle, not a TODO.

---

## The formula

```
computed_confidence =
    (source_count_weight · agreement_score · recency_factor)
    · lifecycle_multiplier
    - contradiction_penalty
```

### Inputs (5 total)

| Input                   | Definition                                                       | Source                                                                              |
|-------------------------|------------------------------------------------------------------|-------------------------------------------------------------------------------------|
| `source_count_weight`   | `min(1, source_count / 5)` — saturates at 5 sources              | DB (`object_sources` join)                                                          |
| `agreement_score`       | Fraction of agreeing sources, `[0, 1]`                           | **EXTERNAL** — produced by `RelationBuilder` (primary) or `Validator` (fallback)    |
| `recency_factor`        | `exp(-age_days / 180)` — half-life of 180 days                    | DB (`created_at` / `updated_at`)                                                    |
| `lifecycle_multiplier`  | `canonical=1.0`, `validated=0.85`, `reviewed=0.7`, `candidate=0.5`, `extracted=0.3`, `raw=0.1`, `deprecated=0` | DB (`status` column)                                                                |
| `contradiction_penalty` | `0.2 · contradicting_relation_count`, capped at `0.8`            | DB (`relations` join, type `contradicts`)                                           |

Output is clamped to `[0, 1]`. The formula is a pure function of its inputs
(spec.md:257). The Compiler is the only writer and never persists the result
(ADR-003, design.md:794-830).

**4 of 5 inputs are DB-derivable. `agreement_score` is the open debt**
(see ADR-002, design.md:756-792, and backlog §2.1).

### Worked examples (from spec.md:269-345)

| Scenario                                            | Result     |
|-----------------------------------------------------|------------|
| 1 source, 1 day old, validated, no contradictions   | `≈ 0.169`  |
| 5 sources, 30 days old, canonical, no contradictions | `≈ 0.8465` |
| 1 source, 365 days old, validated, no contradictions | `≈ 0.0224` |
| 3 sources, 0 days, raw, no contradictions            | `0.06`     |
| 3 sources, 60 days, candidate, 1 contradiction      | `≈ -0.028` → clamped to `0` |

---

## Why derived values are not stored

Five engineering reasons, in order of architectural weight:

1. **No drift.** Storing both a stored value and a derived value that depends
   on changing inputs creates a guaranteed eventual inconsistency. The spec
   explicitly forbids overwriting the stored `confidence` with the computed
   one (spec.md:193). The only enforcement that holds in perpetuity is:
   *never store the derived value*.
2. **Formula evolution without data migration.** When `agreement_score`
   becomes DB-derivable (after change #12 lands), the formula will change.
   Stored values would force a backfill job with ordering, race, and audit
   concerns. Derived values need only a code deploy.
3. **The Compiler stays a pure function** (ADR-002, design.md:776-778). A
   function `f(inputs) -> value` is the cheapest thing to test and reason
   about. Persisting the output couples the write path to the read path
   and introduces "when do we UPDATE?" decisions for every input change.
4. **No recomputation decisions.** If stored: when does it update? On new
   source? On new relation? On lifecycle change? On `next_review_at` change?
   On a cron? Each option introduces locks, races, and ordering rules.
   Derivation sidesteps the entire class.
5. **The computation is cheap.** Five indexed column reads plus arithmetic.
   Sub-microsecond per object. No economic justification for storage.

### When you WOULD store a derived value

- The computation is expensive (aggregations, ML feature stores).
- You need point-in-time snapshots.
- You need to materialize an index (views, materialized tables).

None apply here.

---

## Known debt: `agreement_score`

`agreement_score` has no DB-derivable definition yet. The Compiler treats it
as an external input provided by:

- **Primary producer**: `RelationBuilder` (Worker 5 of the pipeline)
- **Fallback**: `Validator` (Worker 6)

The Compiler stays pure (function of its declared inputs). The fact that
`agreement_score` is not reproducible from DB state is a **known, documented
gap** — not a bug, not a hidden risk. ADR-002 captures the decision and
queues the follow-up to change #12.

When change #12 lands, the closure table + edge weights make a DB-derivable
`agreement_score` definition possible. At that point the external input
goes away and the formula becomes 5/5 reproducible from the DB.

---

## Open decision (backlog §1.1)

`freshness` is the same family as `computed_confidence` — derived on read,
pure function of `next_review_at` and current time. The decision pending
close is **where to expose it on the read API**:

- **Option A** *(default in design.md:844-852)*: top-level field in the read
  response, alongside `confidence` and `computed_confidence`.
- **Option B**: audit-only — only visible in `audit_events`.

Decision captured in the working session that produced this file.

---

## Spec anchors

- `spec.md:189-194` — stored vs computed MUST be distinct values
- `spec.md:218-267` — full formula, inputs, clamping
- `spec.md:269-345` — worked-example scenarios
- `spec.md:411-435` — both values returned on every read
- `spec.md:437-466` — freshness derivation semantics
- `design.md:756-792` — ADR-002 (agreement_score external)
- `design.md:794-830` — ADR-003 (Compiler is the only writer)
- `design.md:844-852` — freshness API surface default
- `internal/domain/knowledge.go:78` — current `Confidence *float64` field
