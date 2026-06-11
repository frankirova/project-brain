# 02-3 — Agents Shared Brain (change #9)

Closes HIGH risk §2.3 of the backlog. Captured 2026-06-10.

---

## TL;DR

Change #9 introduces the `AgentRuntime`: the **single public
write surface** for all specialized agents. The runtime routes
to `RawInputRepository.Insert` + the outbox (per §1.3) +
asynchronous compilation (per change #7). All agent types
(Research, Architect, Documentation, DevOps, Financial, Sales)
go through the same contract.

The open `research-agent` exploration recommended calling
`IngestTextService` directly. **That recommendation is rejected**
here. The new design unifies all agents — including the HTTP
handler and the Telegram handler — behind `Runtime.Write`.

**Eight concrete decisions**:

1. **New package `internal/runtime/`** with `AgentRuntime`
   interface + impl.
2. **`Runtime.Write(ctx, in AgentRawInput) (RawInputID, error)`**
   — returns the `raw_input_id` (NOT the eventual
   `knowledge_object_id`; the object is created asynchronously
   by the Compiler).
3. **All callers go through the runtime** — agents, HTTP
   handler, Telegram handler. `IngestTextService` is refactored
   into a thin shim (or removed entirely).
4. **6 agent types as stubs** in `internal/agent/`, with
   Research as the first concrete impl. Architect,
   Documentation, DevOps, Financial, Sales are placeholder
   implementations.
5. **The Research agent uses the existing `LLMClient` and
   `WebSearcher` ports** (defined in the open exploration).
   Web search + LLM synthesis happen **inside the agent
   package**, before calling the runtime.
6. **The `Source` field on the `raw_inputs` row carries the
   agent identity** for audit. The `owner` column on the
   resulting `knowledge_objects` row carries it for
   provenance. The 7-worker pipeline does NOT branch on agent
   identity.
7. **Async, NATS-driven** (per change #8). The runtime
   publishes `raw_input.received`; the Compiler (change #7)
   consumes `raw_input.validated` and creates the object.
8. **`research-agent` exploration is archived** (or replaced).
   Its port definitions (`LLMClient`, `WebSearcher`) are
   preserved; the implementation goes through the new runtime.

---

## What change #9 needs to do

Per `tasks.md:80-85`, `proposal.md:284-293`, `design.md:410-467`:

- Refactor `research-agent` to write via `Runtime` + `Compiler`
  (NOT via `IngestTextService` directly).
- Add the same contract for architect, documentation, devops,
  financial, sales agents.
- No agent has private memory. No agent writes directly to
  `knowledge_objects`.
- The `Write(ctx, in) (ObjectID, error)` API is the
  architectural guarantee.
- Risk: the `research-agent` open exploration currently
  recommends calling `IngestTextService` directly. That
  recommendation is **rejected**. The change is large because
  the agent boundary has to be redrawn.

---

## The current state (the gap)

### `research-agent` open exploration (the wrong direction)

`openspec/changes/research-agent/exploration.md:96-104`:

> "**Yes — propose `research-agent` as a synchronous HTTP
> endpoint (`POST /v1/research`) that:**
> 1. Accepts a research topic and workspace_id
> 2. Calls web search API to find relevant sources
> 3. Fetches and extracts content from top results
> 4. Calls LLM to synthesize a structured research report
> 5. **Persists the report as a `KnowledgeObject` with `type:
>    'research'` via `IngestTextService`**
> 6. Persists each source URL as a separate `Source` linked to
>    the knowledge object
> 7. Returns the structured result with object_id and source_ids"

The exploration wants the Research agent to call
`IngestTextService` directly. This is the **old paradigm**:
the agent has direct access to the knowledge layer, bypassing
the inbox.

### Why this is wrong (per the new paradigm)

`exploration.md:464-468`:

> "If `research-agent` lands first with its own private memory,
> it will diverge from the shared-brain contract. Mitigation:
> the proposal should list `research-agent` as a dependent
> change, retargeted to `RawInputRepository` + `KnowledgeCompiler`."

The new paradigm says: **all agents are equivalent at the
storage layer**. A Research agent and a Documentation agent
produce indistinguishable `raw_inputs` rows. The agent-
specific logic (web search, LLM synthesis) happens **before**
the runtime call. The runtime is the only door.

If `research-agent` lands first with `IngestTextService`
access, it builds a parallel write path. Subsequent agents
(Architect, etc.) will copy that pattern. The shared-brain
contract becomes a fiction.

The refactor **unifies everything** behind `Runtime.Write`.

---

## Sub-decisions inside change #9

### Decision 1: The `AgentRuntime` interface

Per `design.md:417-420`:

```go
// internal/runtime/agent_runtime.go
type AgentRuntime interface {
    Write(ctx context.Context, in AgentRawInput) (uuid.UUID, error)
}
```

What does the returned UUID represent?

Two interpretations:
- A) The `raw_input_id` (the row that was just inserted). The
  eventual `knowledge_object_id` comes later from the
  Compiler.
- B) The eventual `knowledge_object_id`. Requires the Compiler
  to run synchronously after the inbox insert.

The async pipeline (change #7 + #8) implies **A**. The
runtime inserts into `raw_inputs`, publishes to NATS, and
returns. The Compiler consumes `raw_input.validated` later and
creates the object.

The caller (an agent's HTTP handler, or the agent's own
logic) gets the `raw_input_id`. If they need the
`knowledge_object_id`, they poll or subscribe.

**Decision**: the returned UUID is the `raw_input_id`.
Rename the interface method to `WriteRawInput` for clarity, or
keep `Write` and document the return type carefully.

I'll keep `Write` for symmetry with the spec; the doc comment
explicitly says "returns the raw_input_id, not the eventual
object_id".

### Decision 2: The `AgentRawInput` shape

```go
type AgentRawInput struct {
    WorkspaceID  string
    Source       string                 // "http", "telegram", "research", "architect", etc.
    Content      string                 // the text/idea/observation
    Metadata     map[string]any         // agent-specific
    IdentityKey  string                 // sha256(...); idempotency
    ContentHash  string                 // sha256(Content); for dedup
    OwnerID      string                 // user/agent identifier
}
```

The `Source` field is the agent identity. It's stored on the
`raw_inputs` row for audit. The `OwnerID` is the producer
(user or agent). It's stored on the resulting
`knowledge_objects` row.

### Decision 3: All callers go through the runtime

Today, callers are:
- `httpapi.IngestTextHandler` (HTTP `POST /v1/ingest-text`)
- `telegram.Handler` (Telegram messages)

Tomorrow, callers also include:
- `agent.Research` (Research agent)
- `agent.Architect` (Architect agent)
- `agent.Documentation` (Documentation agent)
- ... (5 more)

**Decision**: ALL of these go through `Runtime.Write`. The
runtime is the only public write surface.

The refactor:
- `IngestTextService` is **deprecated**. It's removed or
  reduced to a thin shim.
- `httpapi.IngestTextHandler` calls `Runtime.Write` directly.
- `telegram.Handler` calls `Runtime.Write` directly.
- Each agent calls `Runtime.Write` after its agent-specific
  work.

### Decision 4: 6 agent types as stubs

```go
// internal/agent/research/research.go
type Research struct {
    runtime  runtime.AgentRuntime
    llm      app.LLMClient
    webSearch app.WebSearcher
    fetcher  app.DocumentFetcher
    logger   *slog.Logger
}

func (r *Research) Research(ctx context.Context, topic string) (uuid.UUID, error) {
    // 1. Web search for sources
    sources, err := r.webSearch.Search(ctx, topic)
    if err != nil { return uuid.Nil, err }
    
    // 2. Fetch content from top sources
    var contents []string
    for _, src := range sources[:5] {
        content, err := r.fetcher.Fetch(ctx, src.URL)
        if err != nil { continue }
        contents = append(contents, content)
    }
    
    // 3. LLM synthesis
    report, err := r.llm.Synthesize(ctx, topic, contents)
    if err != nil { return uuid.Nil, err }
    
    // 4. Persist via runtime
    return r.runtime.Write(ctx, runtime.AgentRawInput{
        WorkspaceID: q.WorkspaceID,
        Source:      "research",
        Content:     report,
        Metadata: map[string]any{
            "topic": topic,
            "sources": sources,
        },
        IdentityKey: computeResearchIdentityKey(q.WorkspaceID, topic),
        ContentHash: sha256(report),
        OwnerID:     "research-agent",
    })
}
```

The Research agent is the only **concrete** agent at first.
Architect, Documentation, DevOps, Financial, Sales are
**stubs** — they exist as types so future code can be
referenced, but they don't have implementations yet.

### Decision 5: Where do the agent-specific ports live?

The agent-specific ports (LLM, WebSearch, DocumentFetcher)
were defined in the open exploration as `internal/app/ports.go`
additions. They stay there. The agents in `internal/agent/`
consume them.

```go
// internal/app/ports.go (additions)
type LLMClient interface {
    Complete(ctx context.Context, prompt string) (string, error)
    Synthesize(ctx context.Context, topic string, sources []string) (string, error)
}

type WebSearcher interface {
    Search(ctx context.Context, query string) ([]SearchResult, error)
}

type DocumentFetcher interface {
    Fetch(ctx context.Context, url string) (string, error)
}
```

Concrete implementations in `internal/<provider>/` packages
(e.g., `internal/openai/`, `internal/brave/`).

### Decision 6: Agent identity on the `raw_inputs` row

Per `design.md:459-467`:

> "All agents are equivalent at the storage layer. A Research
> agent call and a Documentation agent call produce
> indistinguishable `raw_inputs` rows. The `Source` field on
> the `raw_inputs` row carries the agent identity for audit,
> but the rest of the pipeline does not branch on it. The
> `owner` column on the resulting `knowledge_objects` row
> carries the agent identity for provenance."

**Decision**:
- `raw_inputs.source` = `"research"` / `"architect"` / etc.
- `raw_inputs.metadata.agent` = same value (for redundancy)
- The 7-worker pipeline (Normalizer, Classifier, etc.) does
  NOT branch on this. It treats all inputs the same.
- The resulting `knowledge_objects.owner` = the agent
  identity (e.g., `"research-agent"`).
- The audit log records both the source (raw_inputs row) and
  the owner (knowledge_objects row).

### Decision 7: Async, NATS-driven

The runtime publishes `raw_input.received` to NATS (via the
outbox, per §1.3). The 7-worker pipeline consumes. The
Compiler (change #7) consumes `raw_input.validated` and
creates the object.

The agent's caller (e.g., `POST /v1/research`) gets back the
`raw_input_id`. To get the `object_id`, they:
- Poll the inbox status: `GET /v1/raw-inputs/{id}` returns
  `status` and (when compiled) the linked `object_id`.
- Or subscribe to `object.canonicalized` events (for
  high-priority cases).

**Decision**: simple polling endpoint. The HTTP handler
returns `{raw_input_id, status_url}` and the caller polls.
The Telegram bot uses a similar pattern.

### Decision 8: `research-agent` exploration is archived

The open exploration at
`openspec/changes/research-agent/exploration.md` is **archived**
(or replaced). The port definitions it introduced (`LLMClient`,
`WebSearcher`, `DocumentFetcher`) are **preserved** — they move
to `internal/app/ports.go` and become part of the app ports.

The implementation in the exploration ("call IngestTextService
directly") is **rejected**. The new implementation goes through
`Runtime.Write`.

The exploration can be kept as historical context but its
proposal/tasks should NOT be created. Instead, change #9
absorbs the relevant pieces.

---

## What goes in change #9

1. **New package `internal/runtime/`**:
   - `agent_runtime.go` — the `AgentRuntime` interface
   - `runtime.go` — the impl
   - `agent_raw_input.go` — the `AgentRawInput` type
2. **New package `internal/agent/`** with 6 stub agents:
   - `research/` — concrete impl (the only one)
   - `architect/` — stub
   - `documentation/` — stub
   - `devops/` — stub
   - `financial/` — stub
   - `sales/` — stub
3. **Update `internal/app/ports.go`** to add `LLMClient`,
   `WebSearcher`, `DocumentFetcher` ports.
4. **New package `internal/openai/`** (LLMClient impl) and
   `internal/brave/` (WebSearcher impl) and
   `internal/fetcher/` (DocumentFetcher impl). Stub impls
   for tests.
5. **Refactor `internal/app/ingest_text.go`** — reduce
   `IngestTextService` to a thin shim around `Runtime.Write`,
   or remove it entirely.
6. **Refactor `internal/httpapi/ingest_text.go`** to call
   `Runtime.Write` directly.
7. **Refactor `internal/telegram/handler.go`** to call
   `Runtime.Write` directly.
8. **Wire the runtime** in `cmd/api/main.go`.
9. **Add polling endpoint** `GET /v1/raw-inputs/{id}` that
   returns `{raw_input_id, status, object_id?}`.
10. **Tests**:
    - `internal/runtime/runtime_test.go` — happy path, dup
      path, error path
    - `internal/agent/research/research_test.go` — research
      flow with fakes
    - Refactor existing tests of `IngestTextService` to use
      the new path
11. **Update the `research-agent` exploration** to mark it as
    superseded by change #9. Or move it to
    `openspec/changes/archive/`.
12. **Update `design.md`** to document the runtime
    architecture.
13. **Update `tasks.md`** to mark #9 done.

---

## What this is NOT

- **Not a new pipeline**. The pipeline is the 7-worker
  contract. The agents are producers of raw_inputs, not
  participants in the pipeline.
- **Not a new write surface**. The runtime IS the only
  write surface. The agents are *callers* of the runtime,
  not *alternatives* to it.
- **Not a refactor of the Telegram bot**. The Telegram
  handler is refactored to use the runtime, but the bot's
  user-facing behavior is unchanged.
- **Not a UI change**. No new endpoint, no new field. The
  existing `POST /v1/ingest-text` endpoint still works; it
  just goes through a different code path internally.
- **Not a migration**. The schema is unchanged. The
  `raw_inputs` table already exists (per change #5).
- **Not the `telegram-validation-ui` change** (#14). That's
  about inline-keyboard review. This change is about the
  runtime.

---

## Risks and edge cases

### Risk 1: Agent divergence

If a new agent is added without conforming to the runtime,
it bypasses the inbox. Mitigation:
- The runtime is the **only** public write surface. New
  agents must take it as a constructor dependency.
- Code review + smoke test: grep for `RawInputRepository.Insert`
  callsites; only `internal/runtime/` should have them.

### Risk 2: Agent-specific state

What if an agent needs to store state (e.g., conversation
context for multi-turn research)?

- Agent state goes in the agent's own storage (not the
  shared brain).
- The shared brain is for **knowledge** (raw_inputs →
  knowledge_objects). The agent's state is for **control
  flow** (multi-turn, retries, etc.).
- A Research agent doing multi-turn research stores the
  conversation history in its own DB (or Redis, or whatever).
  Each turn produces a `raw_input` and goes through the
  runtime.

### Risk 3: LLM cost / latency

Each agent invocation may call the LLM multiple times
(search query generation, summarization, entity extraction).
Costs accumulate. Latency varies. Mitigation:
- Configurable model selection (cheap model for extraction,
  expensive for synthesis).
- Caching of search results.
- Rate limiting in the adapter.
- The agent's work happens BEFORE the runtime call, so the
  runtime's latency is bounded (just an INSERT + outbox
  write).

### Risk 4: Web search rate limits

Free tiers (Brave, Tavily) have strict limits. Mitigation:
- Rate limiting in the adapter.
- Cache recent searches.
- Configurable API key.

### Risk 5: Agent takes the runtime hostage

If one agent is slow or buggy, does it affect others? No:
- The runtime is stateless (the only state is the DB).
- Each agent's work happens before the runtime call.
- A slow agent blocks its own caller, not the runtime.
- The runtime's bottleneck is the DB, not the agents.

### Risk 6: Polling endpoint abuse

`GET /v1/raw-inputs/{id}` could be abused (someone polls
1000s of times). Mitigation:
- Rate limiting on the endpoint.
- The status is also visible via NATS subscription (for
  internal use).
- Cache the status for 1 second.

### Risk 7: `Source` enum sprawl

If we add many agent types over time, the `Source` field
becomes a long list. Mitigation:
- Keep the agent types as a small enum (currently 6).
- Add new agent types only with explicit review.
- If the list grows beyond 10, switch to a separate
  `agent_type` table with FK.

---

## Connection to other changes

| Change | Connection |
|---|---|
| #5 raw-inputs-inbox | **Prerequisite**: the runtime writes to `raw_inputs`. |
| #7 knowledge-compiler | **Prerequisite**: the runtime's `raw_input.received` is consumed by the Compiler. |
| #8 event-bus-nats | **Prerequisite**: the runtime uses the outbox. |
| #10 hybrid-retrieval-wiring | Indirect: agents retrieve via the composite retriever. |
| #11 hnsw-embedding-index | Independent. |
| #12 relations-bidirectional-freshness | Independent. |
| #13 audit-duplicate-write | Indirect: the runtime's path for duplicates is what #13 fixed. |
| #14 telegram-validation-ui | Independent. UI change. |

---

## What this DOES enable

- **All 6 agents** (Research, Architect, Documentation,
  DevOps, Financial, Sales) have a single contract to follow.
  Future agents (Coder, Founder, etc.) can be added in the
  same pattern.
- **The HTTP handler and the Telegram handler** are also
  callers of the runtime. Uniformity: all writes go through
  the same door.
- **The `research-agent` exploration is closed** with the
  correct architecture.
- **Specialized agent logic** (web search, LLM synthesis) is
  in the agent packages. The runtime doesn't care.
- **The `knowledge_objects` row** carries the agent identity
  in `owner` for provenance. The audit log records the
  source. Both are queryable.

---

## Spec anchors

- `openspec/changes/paradigm-knowledge-os/proposal.md:284-293` — Agent Runtime
- `openspec/changes/paradigm-knowledge-os/design.md:410-467` — Agent Runtime interaction model
- `openspec/changes/paradigm-knowledge-os/tasks.md:80-85` — change #9
- `openspec/changes/paradigm-knowledge-os/exploration.md:460-468` — research-agent risk
- `openspec/changes/research-agent/exploration.md` — the open exploration (to be archived)
- `research/01-3-transactional-outbox.md` — outbox used by the runtime
- `research/02-1-knowledge-compiler.md` — Compiler (the consumer of runtime's events)
- `research/02-2-event-bus-nats.md` — bus substrate
