## Exploration: Project Brain MVP

### Current State
The repository is planning-only. `PROJECT_BRAIN.md` defines Project Brain as an event-driven, multi-agent knowledge platform, not a chatbot with memory. SDD/OpenSpec is initialized in `openspec/config.yaml` with OpenSpec artifact persistence, interactive execution, auto-forecast chained PR strategy, a 400-line review budget, and `strict_tdd: false` because no code, test runner, or implementation stack exists yet.

The product vision recommends a Go backend, PostgreSQL-first knowledge core, pgvector, PostgreSQL full-text search, graph-like relations, auditable knowledge artifacts, Telegram as an initial interface, and incremental event-driven processing. The MVP goal in the source document is to capture knowledge from Telegram and query it with basic hybrid RAG, but that whole MVP is too large for a first reviewable implementation slice.

### Affected Areas
- `PROJECT_BRAIN.md` — foundational product and architecture source for MVP scope, domain model, storage strategy, pipeline, risks, and key decisions.
- `openspec/config.yaml` — active SDD/OpenSpec project context, rules, testing capability state, and review-size guardrails.
- `openspec/specs/` — currently empty source-of-truth specs; later phases should introduce delta specs before implementation.
- `openspec/changes/project-brain-mvp/` — active change folder for this MVP exploration and subsequent proposal/spec/design/tasks artifacts.

### Approaches
1. **Telegram-first save loop** — Build a Telegram bot plus minimal ingestion path for `/save`, persisting raw messages as knowledge objects and sources.
   - Pros: Validates the lowest-friction user entry point; matches the product's capture-first adoption risk; creates visible demo value quickly.
   - Cons: Risks coupling early design to Telegram if the gateway boundary is weak; requires bot setup before core domain behavior is proven; retrieval value remains limited without search.
   - Effort: Medium

2. **Knowledge Core-first foundation** — Bootstrap the backend, PostgreSQL schema, migrations, repository boundaries, and minimal CRUD for `knowledge_objects`, `sources`, `relations`, and `audit_events`.
   - Pros: Respects the platform vision; establishes auditable storage and domain primitives before agents or channels; easiest slice to keep reviewable and testable once code exists.
   - Cons: Less flashy than a bot demo; does not yet prove end-user capture/retrieval workflows; needs careful scope control to avoid modeling the whole future platform.
   - Effort: Medium

3. **Hybrid retrieval-first prototype** — Seed knowledge objects manually, then implement keyword search, structured filters, and a placeholder interface for future vector search.
   - Pros: Validates the "not only embeddings" principle early; produces useful answers with citations sooner; informs schema and indexing needs.
   - Cons: Requires stored data first; vector search and graph expansion can easily exceed the first-slice budget; less valuable if ingestion remains manual-only.
   - Effort: Medium-High

4. **Event pipeline skeleton** — Define internal events and a synchronous in-process processor that normalizes, classifies minimally, persists, indexes, and audits one input type.
   - Pros: Protects the event-driven direction without introducing NATS immediately; creates seams for future workers and reprocessing; avoids a CRUD-only dead end.
   - Cons: Abstract infrastructure can become overengineering before the first user workflow; event contracts need domain specs first; harder for reviewers if combined with storage and Telegram.
   - Effort: Medium-High

### Recommendation
Start with **Knowledge Core-first foundation**, but shape it around one concrete workflow: `save text knowledge via an interface-neutral ingestion command`. The first MVP slice should bootstrap only the smallest platform core needed to persist a user-provided text item as an auditable knowledge object with its source and minimal metadata.

The recommended first slice should include:
- A minimal Go service/application skeleton and test setup.
- PostgreSQL migration support.
- Core tables for `knowledge_objects`, `sources`, `object_sources`, and `audit_events`.
- An interface-neutral ingestion boundary for plain text input.
- Persistence of a `source` plus one `knowledge_object` with type, title/summary/content, workspace scope, metadata, and audit event.
- A thin CLI or HTTP endpoint only if needed to exercise the ingestion boundary; Telegram should remain a later adapter, not the first coupling point.

This is the smallest valuable slice because it creates the platform's durable center: structured, auditable memory. It intentionally defers Telegram, embeddings, NATS, multi-agent orchestration, S3, and full hybrid retrieval until the core object/source/audit contract is real. It still respects the vision because every later capability depends on this knowledge core and can attach through adapters or event processors.

### Risks
- The first slice can become too large if it includes Telegram, embeddings, relations, workers, and retrieval together.
- A pure CRUD implementation would violate the platform vision unless the ingestion boundary and audit trail are explicit from the start.
- The domain model in `PROJECT_BRAIN.md` is broad; proposal/spec phases must narrow mandatory fields and defer non-essential entities.
- No test runner exists yet, so the first implementation slice must include testing/tooling bootstrap before meaningful verification is possible.
- PostgreSQL plus migrations may exceed the 400-line review budget if combined with application scaffolding and adapters; chained PR forecasting should happen during task planning.

### Ready for Proposal
Yes — propose `project-brain-mvp` as an interface-neutral Knowledge Core ingestion slice. The proposal should explicitly state that Telegram capture, hybrid RAG, embeddings, NATS, object storage, and specialized agents are out of scope for the first implementation slice, while preserving extension seams for them.
