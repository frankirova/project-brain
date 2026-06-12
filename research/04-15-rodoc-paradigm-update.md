# 04-15 — ROADMAP Paradigm Update (quick win #15)

Closes quick-win #15 of the backlog. Captured 2026-06-10.

---

## TL;DR

`ROADMAP.md` reflects the **old paradigm** (Fase 2 = Hybrid RAG,
Fase 3 = Human-in-the-Loop with `raw-inputs-table` as "Low" effort,
Fase 4 = Multi-agent). The **new paradigm** (`paradigm-knowledge-os`
change) inverts the order: **inbox first**, then Hybrid RAG, then
agents, then UI. The doc is materially out of date.

The fix is **doc-only** (~half a day):

1. Insert a new "Fase 2 — Paradigm Shift" between current Fase 1
   and the current "Fase 2 — Hybrid RAG".
2. List the 15 changes of the new paradigm under Fase 2, grouped
   by the 5 execution tiers in `paradigm-knowledge-os-backlog.md` §4.
3. Renumber the rest:
   - Current "Fase 2 — Hybrid RAG" → **Fase 3**
   - Current "Fase 3 — Human-in-the-Loop" → **Fase 5**
   - Current "Fase 4 — Multi-agent" → **Fase 4** (stays)
   - New "Fase 6 — Dashboard web" (was implicit in Fase 4)
4. Update the H2 finding (`ROADMAP.md:233`) — resolved by #13.
5. Update the "Fase X progreso" sections to show quick wins
   (#11, #13) as done.
6. Keep Fase 1 untouched (it IS done).

**No code change. No migration. No new file.**

---

## The exact gap (cite + line)

From `design.md:703-707`:

> "**`ROADMAP.md` ordering is wrong for the new paradigm.**
> Fase 2 lists Hybrid RAG as half-done; Fase 3 lists
> `raw-inputs-table` as Low effort, when under the new
> paradigm it is foundational."

Concrete drift points:

### Drift 1: Phase numbering (`ROADMAP.md:52-148`)

Current ordering:
- Fase 1: Foundation (✅ done)
- **Fase 2: Hybrid RAG** (claimed "siguiente", but the new paradigm
  puts inbox first)
- **Fase 3: Human-in-the-Loop** (with `raw-inputs-table` as "Low"
  effort, line 133)
- Fase 4: Multi-agent Platform

New ordering (per `paradigm-knowledge-os-backlog.md` §4):
- Fase 1: Foundation (✅)
- **Fase 2: Paradigm Shift** (15 changes — the new architecture)
- Fase 3: Hybrid RAG (now genuinely "siguiente", after the inbox
  is in place)
- Fase 4: Multi-agent Platform
- Fase 5: Human-in-the-Loop
- Fase 6: Dashboard web

### Drift 2: H2 finding still open (`ROADMAP.md:233`)

> "H2 | Duplicates no dejan audit trail | ⏳ Pendiente"

Change #13 closes this. The ROADMAP should reflect that:
"H2 | Duplicates no dejan audit trail | ✅ Resuelto: change #13
(`audit-duplicate-write`)".

### Drift 3: "Fase X progreso" out of date (`ROADMAP.md:186-194`)

Says:
- ✅ `Retriever` port (C4 cerrado)
- ✅ `FTSRetriever` + endpoint
- ✅ Embeddings con pgvector
- ✅ `CompositeRetriever` con RRF
- ✅ Hydration del composite
- ✅ `/v1/objects/{id}` endpoint
- ⏳ Wire del composite en main.go
- ⏳ Tests integration del composite contra Postgres real

But:
- The HNSW index is missing (quick win #11 not done).
- The `pending_events` table doesn't exist (change #8 not done).
- The `raw_inputs` inbox doesn't exist (change #5 not done).
- The 7-worker pipeline is a spec, not code.

The "Fase X progreso" should not claim things that aren't done.
Either soften the language ("spec'd, not wired") or move them
to Fase 2 — Paradigm Shift with a "spec'd" status.

### Drift 4: References to old terms

The doc still uses:
- "knowledge nodes" / `knowledge_nodes` (`ROADMAP.md:113-114`) —
  wrong. The new paradigm uses `knowledge_objects` with a
  7-state lifecycle, not a 3-state `proposed → debating → validated`.
- "raw_inputs ... No existe" (`ROADMAP.md:113`) — will become
  "✅ exists after #5".
- "knowledge_nodes ... Parcial (hoy es `knowledge_objects` con
  status libre)" (`ROADMAP.md:114`) — wrong. The 7-state
  lifecycle IS the spec; the change set implements it.

---

## Sub-decisions inside the doc update

### 4.1 Doc structure: insert vs rewrite

| Approach | Pros | Cons |
|---|---|---|
| **A. Insert + renumber (recommended)** | Preserves Fase 1 history; minimal diff | Renumbering is a lot of small text edits |
| B. Full rewrite of "current vs new" sections | Cleaner | Erases history; harder to review |
| C. Add a "Paradigm Update" section that supersedes the old | Less disruptive | Two competing "current" sections = confusion |

→ **A — insert + renumber**. The diff is large but each change is
small and reviewable.

### 4.2 Where to put quick wins #11, #13, #15 in the new ordering

Per `paradigm-knowledge-os-backlog.md` §4.1, quick wins are
"independientes (sin dependencias, ½ día cada uno)". They can
land anywhere in the new paradigm, in any order.

In the new ROADMAP:
- **#11 (HNSW index)** — listed under Fase 2.5 "Quick wins" or as
  a sub-section of "Paradigm Shift → Pipeline + bus". It's a
  perf fix for the existing `embeddings` table.
- **#13 (audit duplicate)** — listed under Fase 2.5 or as a
  sub-section of "Paradigm Shift → Core del dominio" (it's about
  the inbox path).
- **#15 (rodoc update)** — listed as the LAST item of Fase 2
  (it documents the rest).

**Recommendation**: put all three in a sub-section "Fase 2.5 — Quick
wins (hygiene)" with effort and status.

### 4.3 How to represent "in progress" vs "spec'd"

Three states:
- ✅ **Done** — code in main, tests green, archived change.
- 🔨 **In progress** — code in a branch, PR open or in review.
- 📝 **Spec'd** — proposal/design/tasks exist, no code yet.

The current ROADMAP conflates these. The new one should
distinguish them clearly. Use emoji or status text per row.

### 4.4 What to do with the H1, M*, L* findings

The audit section (lines 215-272) has CRITICAL / HIGH / MEDIUM /
LOW findings. Most are resolved. A few are still open:
- H2: resolved by #13 (this update)
- M6: `audit_events` schema listo, wire pendiente
- M8: `Metadata` para context extra (in progress with #13)
- M11: callback query stub (in progress with #14)
- L2, L5: bajo valor, skip

The doc should mark these per current state, with the change
that closes them.

### 4.5 What about the "Fase 4 progreso" section?

The current "Fase 4 — Multi-agent Platform" (`ROADMAP.md:151-174`)
is forward-looking, no in-progress items. Just list the planned
agents and changes. The new paradigm renames to "Fase 4" and
references the `agents-shared-brain` change (#9) as the bridge
between Fase 2 (pipeline) and Fase 4 (agents).

### 4.6 Length budget

The current ROADMAP is 294 lines. The new one will be 350-400
lines (more granular Fase 2 with 15 changes). That's fine for a
"living roadmap" doc. Don't artificially trim.

---

## What goes in change #15

1. **Edit `ROADMAP.md`**:
   - Lines 50-148: restructure to insert "Fase 2 — Paradigm Shift"
     between Fase 1 and the current "Fase 2 — Hybrid RAG".
   - Renumber "Hybrid RAG" → Fase 3, "Human-in-the-Loop" → Fase 5,
     "Multi-agent" → stays Fase 4, add "Fase 6 — Dashboard web".
   - Lines 186-194: update "Fase X progreso" to show actual state.
   - Line 233: mark H2 as ✅ Resuelto.
   - Lines 250, 252, 255: update M6, M8, M11 per current state.
   - Lines 113-115: update the "raw_inputs / knowledge_nodes /
     sdd_documents" section to reflect the new paradigm.

2. **No new files**. ROADMAP.md is updated in place.

3. **No new migrations**.

4. **No spec changes**. The new ordering is the spec; the doc
   just reflects it.

5. **Commit**: a single commit with a message like
   `docs: update ROADMAP.md for paradigm-knowledge-os ordering`.

---

## What this is NOT

- **Not a new paradigm**. The paradigm shift is in
  `openspec/changes/paradigm-knowledge-os/`. This is the doc
  that reflects it.
- **Not a re-architecture**. No code change.
- **Not a final state**. The doc will continue to evolve as
  changes land. This is a "current truth" snapshot.
- **Not a project retrospective**. The new ordering is forward-
  looking; it doesn't judge the old ordering.

---

## Risks and edge cases

### Risk 1: Doc drift after the change

The ROADMAP is a living document. After this update, the same
drift can happen again. The mitigation is **process**:
- Update ROADMAP as part of every change's archive step.
- Add a "ROADMAP delta" line in the commit message of every
  meaningful change.

This is a process change, not a doc change. It's OK to propose it
in the change but not enforce it.

### Risk 2: Breaking cross-references

Other docs (PROJECT_BRAIN.md, AGENTS.md, READMEs) may reference
ROADMAP by section number ("see ROADMAP.md Fase 2"). Renumbering
breaks those references. Mitigation:
- Grep for `ROADMAP.md` references in the repo.
- Update each one (or note them in the commit message).

### Risk 3: Inconsistency with the change artifacts

The ROADMAP says things; the change artifacts say things. If they
disagree, who's right? The change artifacts are the source of
truth (proposal + design + tasks). The ROADMAP should mirror
them. After the update, double-check each line of the new
Fase 2 against the `paradigm-knowledge-os` change artifacts.

### Risk 4: Doc bloat

The new ROADMAP will be longer (15 changes listed in Fase 2 vs
the current 4). Mitigation: use tables, not paragraphs. Tables
are scannable and don't bloat visually.

---

## What this DOES enable

- **Onboarding**: a new dev reads ROADMAP.md and sees the new
  paradigm's order, not the old one. No more "why is `raw-inputs`
  in Fase 3 if it's foundational?" questions.
- **Project status**: the H2 finding is closed, the quick wins
  are reflected, the new ordering is documented. Project
  status is honest.
- **Planning**: the next change to work on is visible at a
  glance. The 5 execution tiers (4.2 base, 4.3 core, 4.4
  pipeline, 4.5 agents+UI) give a natural sprint plan.

---

## What the new ROADMAP should look like (sketch)

```markdown
# Roadmap — Project Brain

## ✅ Fase 1 — Foundation (completa)
[unchanged]

## 🔄 Fase 2 — Paradigm Shift (in progress)
The architecture pivot: inbox first, hybrid RAG second, agent
framework third. Documented in detail in
`openspec/changes/paradigm-knowledge-os/`.

### 2.1 Quick wins (hygiene) — independent, ½ día c/u
- #11 hnsw-embedding-index ✅
- #13 audit-duplicate-write ✅
- #15 rodoc-paradigm-update ✅ (this change)

### 2.2 Base fundacional (rompe tests legacy)
- #1 workspace-id-uuid-migration (2 días)
- #2 lifecycle-states-migration (5-10 días)

### 2.3 Core del dominio
- #3 versioning-and-versions-table
- #4 freshness-and-owner
- #5 raw-inputs-inbox (incl. test rename)
- #6 lifecycle-events-log

### 2.4 Pipeline + bus
- #7 knowledge-compiler (multi-semana)
- #8 event-bus-nats
- #10 hybrid-retrieval-wiring
- #12 relations-bidirectional-and-freshness

### 2.5 Capa de agentes + UI
- #9 agents-shared-brain
- #14 telegram-validation-ui

## 🔜 Fase 3 — Hybrid RAG (siguiente, blocked by Fase 2)
[the current Fase 2 content, lightly updated]

## 🤖 Fase 4 — Multi-agent Platform (futuro, blocked by Fase 3)
[the current Fase 4 content, lightly updated]

## 🎯 Fase 5 — Human-in-the-Loop Validation (futuro, blocked by Fase 4)
[the current Fase 3 content, renumbered]

## 🌐 Fase 6 — Dashboard web (futuro)
[briefly: graph explorer, search UI, validation UI]

## 📊 Estado del roadmap
[updated status board]

## 🔍 Deuda técnica conocida
[updated with H2 resolved, M6/M8/M11 in progress]
```

Estimated new length: ~370 lines. +76 from current 294. Mostly
tables, not paragraphs.

---

## Spec anchors

- `ROADMAP.md:1-294` — current state (the file to update)
- `openspec/changes/paradigm-knowledge-os/proposal.md:445-454` —
  change #13 description
- `openspec/changes/paradigm-knowledge-os/design.md:703-707` —
  the original gap analysis
- `openspec/changes/paradigm-knowledge-os/tasks.md:111-114` —
  change #15 task
- `openspec/changes/paradigm-knowledge-os/exploration.md:557-559`
  — exploration findings
- `research/paradigm-knowledge-os-backlog.md` §4 — new ordering
  with 5 execution tiers
- `research/04-11-hnsw-embedding-index.md` — quick win #11
- `research/04-13-audit-duplicate-write.md` — quick win #13
