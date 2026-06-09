## Exploration: Research Agent

### Current State

The codebase has a clean three-layer architecture with a single ingestion pipeline:

- **Domain** (`internal/domain/knowledge.go`): `Source`, `KnowledgeObject`, `ObjectSource`, `AuditEvent` structs. `IngestTextRequest`/`IngestTextResult` for the HTTP API. Knowledge object types are extensible via string constants (`document`, `research`, `decision`, etc.) but only `document` is actively used.
- **Application** (`internal/app/`): `IngestTextService.Ingest(ctx, IngestTextRequest) (IngestTextResult, error)` — the only use case. It persists Source + KnowledgeObject + ObjectSource + AuditEvent in a transaction. Idempotent via `identity_key`.
- **Infrastructure** (`internal/postgres/`): pgx-backed `DB` implementing `IngestionUnitOfWork`, with repository implementations for the four entity types.
- **HTTP** (`internal/httpapi/`): `POST /v1/ingest-text` and `GET /v1/health` endpoints.
- **Composition** (`cmd/api/main.go`): Wires config, PostgreSQL (or in-memory fake), service, handler, mux, and graceful shutdown.

**What exists**: A working ingestion pipeline that accepts raw text with metadata and persists it as a `KnowledgeObject` linked to a `Source`. The domain model supports the `research` object type but nothing in the codebase generates it.

**What does NOT exist**:
- No agent layer, no orchestrator, no agent interfaces
- No web search capability (no HTTP client for external APIs)
- No LLM integration (no OpenAI/Anthropic client)
- No document fetching (URLs, PDFs, web pages)
- No scheduling or job queue
- No knowledge retrieval/search (no query use case)
- No relations table in the DB (migration only has `sources`, `knowledge_objects`, `object_sources`, `audit_events`)
- No `embeddings`, `chunks`, `relations`, `object_versions` tables

The Research Agent must bridge the gap between "user requests a topic" and "structured knowledge is automatically persisted."

### Affected Areas

- `internal/domain/knowledge.go` — may need new types for research-specific structures (ResearchResult, SourceRef, etc.)
- `internal/app/ports.go` — new ports for external capabilities (WebSearcher, DocumentFetcher, LLMClient)
- `internal/app/` (new) — `research_agent.go` or `research_service.go` containing the research use case
- `internal/httpapi/` — new endpoint `POST /v1/research` to trigger research
- `cmd/api/main.go` — wire new dependencies (LLM client, web search, etc.)
- `migrations/` — new migration for `relations` table if the agent creates relations
- `internal/` (new packages) — adapter implementations for web search, LLM, document fetching

### Approaches

1. **Synchronous HTTP endpoint — POST /v1/research** — User sends a topic, the API blocks while the agent researches, then returns the structured result. The agent internally calls external APIs (search, LLM), synthesizes findings, and persists via `IngestTextService`.
   - Pros: Simplest integration; existing `IngestTextService` is reused directly; no new infra (no queue, no workers); immediate feedback to caller; fits the current single-process model; easy to test end-to-end.
   - Cons: Long request latency (research can take 30-120 seconds); HTTP timeout risk; blocks a goroutine during external calls; no retry/resume if the process crashes mid-research; not suitable for scheduled/background research.
   - Effort: Low-Medium

2. **Async job with internal channel queue** — `POST /v1/research` returns a `job_id` immediately. A background goroutine (or worker pool) processes the research job. A `GET /v1/research/{job_id}` polls for status. Jobs are held in-memory or in a PostgreSQL `research_jobs` table.
   - Pros: Non-blocking; supports long-running research; status polling; can persist job state for crash recovery; fits future event-driven architecture; supports scheduled research later.
   - Cons: More complex (job state machine, polling endpoint, worker lifecycle); requires job persistence for reliability; needs idempotency for retries; in-memory queue lost on restart unless persisted.
   - Effort: Medium

3. **Event-driven with NATS** — `POST /v1/research` publishes a `ResearchRequested` event to NATS. A Research Agent worker subscribes, processes the research, and publishes `ResearchCompleted` with results. The API exposes a subscription or polling endpoint for results.
   - Pros: Decoupled; scalable; aligns with PROJECT_BRAIN.md's event-driven vision; supports multiple agent types naturally; crash-safe with NATS JetStream.
   - Cons: Adds NATS as a hard dependency (not yet in go.mod); requires JetStream for persistence; more operational complexity; over-engineered for a single agent type at this stage.
   - Effort: Medium-High

### Recommendation

**Start with Approach 1 (Synchronous HTTP endpoint)** as the MVP slice, then evolve to Approach 2 (Async job) as the next increment.

Rationale:
- The user's vision is "AI agent does research and AUTOMATICALLY saves structured knowledge." The key word is *automatically* — the user triggers research, the system does the rest. A synchronous endpoint delivers this immediately with minimal new infrastructure.
- The `IngestTextService` is already clean and ready to be called programmatically by the agent. No new persistence layer needed.
- External dependencies (web search API, LLM client) are injected via ports, keeping the core testable.
- The 30-120s latency is acceptable for a `/research` command — Telegram commands often have this kind of latency and users expect it.
- When the system needs background research, scheduled digests, or multiple agent types, migrate to async jobs (Approach 2). The port interfaces remain the same; only the orchestration layer changes.

**Critical prerequisite**: The Research Agent needs a way to call an LLM for summarization/extraction. This is the one external dependency that cannot be faked or deferred. The design must define an `LLMClient` port interface early.

### External Dependencies Required

| Dependency | Purpose | Options | Effort to Integrate |
|-----------|---------|---------|---------------------|
| Web Search | Find information on a topic | Brave Search API, SerpAPI, Tavily, DuckDuckGo (unofficial) | Low (HTTP client wrapper) |
| Document Fetcher | Read URLs, extract text | `net/http` + `goquery` for HTML, `pdfcpu` for PDF | Low-Medium |
| LLM Client | Summarize, extract entities, classify | OpenAI API, Anthropic API, local Ollama | Medium (API design matters) |
| Scheduling | Trigger research on schedule | Cron in-process, PostgreSQL job table, NATS (future) | Low (defer to async phase) |

### Existing Patterns to Reuse

- **Port pattern** (`internal/app/ports.go`): Define `WebSearcher`, `DocumentFetcher`, `LLMClient` as interfaces. Implementations live in `internal/` adapter packages.
- **UnitOfWork pattern**: The agent's research results persist via the same `IngestionUnitOfWork` → `IngestionRepositories` pipeline.
- **Composition root** (`cmd/api/main.go`): Wire real or fake implementations based on environment.
- **Config pattern** (`internal/config/config.go`): Add API keys, model selection, search provider config via env vars.
- **Domain types**: Use existing `KnowledgeObject` with `type: "research"` and structured `content` (markdown). Use `Source` with `type: "web"` and URI for search results.

### Risks

- **LLM cost and latency**: Each research invocation calls an LLM multiple times (search query generation, summarization, entity extraction). Costs accumulate; latency varies. Mitigation: configurable model selection (cheap model for extraction, expensive for synthesis), caching of search results.
- **Search API rate limits**: Free tiers (Brave, Tavily) have strict limits. Mitigation: rate limiting in the adapter, cache recent searches, configurable API key.
- **Content quality**: LLM-generated research summaries may hallucinate or miss context. Mitigation: store sources with confidence scores, always cite URLs, mark content as AI-generated in metadata.
- **Idempotency**: The same research topic requested twice should not create duplicate knowledge objects. Mitigation: use `idempotency_key` in the existing `IngestTextRequest` (e.g., `research:<topic-hash>`).
- **Scope creep**: The architecture doc describes Research, Architect, Coder, Founder agents. This change should deliver ONLY the Research Agent's core capability. Other agents are future changes.
- **Missing persistence tables**: `relations` table is not in the current migration. The Research Agent may want to create relations between knowledge objects. Mitigation: defer relation creation to the Knowledge Processor agent (Phase 2); for now, persist research as standalone knowledge objects.

### Ready for Proposal

Yes — propose `research-agent` as a synchronous HTTP endpoint (`POST /v1/research`) that:

1. Accepts a research topic and workspace_id
2. Calls web search API to find relevant sources
3. Fetches and extracts content from top results
4. Calls LLM to synthesize a structured research report
5. Persists the report as a `KnowledgeObject` with `type: "research"` via `IngestTextService`
6. Persists each source URL as a separate `Source` linked to the knowledge object
7. Returns the structured result with object_id and source_ids

**Out of scope for this change**: Async job queue, scheduled research, relation creation, other agent types (Architect, Coder, Founder), embedding generation, graph relationships, retry/resume logic.
