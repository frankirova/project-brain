# Proposal: Project Brain MVP Knowledge Core Ingestion

## Intent

Create the first reviewable Project Brain slice: an interface-neutral ingestion foundation that stores plain text as auditable knowledge, not as chatbot memory.

## Scope

### In Scope
- Minimal Go service/application skeleton and test setup.
- PostgreSQL migration support for the first knowledge core tables.
- Persist one text ingestion command as `source` + `knowledge_object` + `object_sources` link + `audit_event`.
- Workspace-scoped metadata for type, title/summary/content, status, and timestamps.

### Out of Scope
- Telegram bot, web UI, CLI polish, and channel-specific workflows.
- Embeddings, RAG, FTS, graph relations, NATS, S3/object storage, workers, and complete agents.
- Full Project Brain domain model beyond the first ingestion contract.

## Capabilities

### New Capabilities
- `knowledge-core-ingestion`: Interface-neutral ingestion of plain text into auditable knowledge objects and sources.

### Modified Capabilities
- None.

## Approach

Bootstrap the smallest backend core around an ingestion boundary. The boundary accepts plain text plus source/workspace metadata, creates the source and knowledge object transactionally, links them, and records an audit event. Keep adapters thin or absent so Telegram and future interfaces plug in later without shaping core behavior.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `PROJECT_BRAIN.md` | Reference | Source for domain names and deferred architecture. |
| `openspec/specs/knowledge-core-ingestion/spec.md` | New | Future source-of-truth capability spec. |
| backend/service paths TBD | New | Go skeleton, ingestion boundary, migrations, tests. |
| database migrations TBD | New | Tables for sources, knowledge objects, object-source links, audit events. |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Slice grows into Telegram/RAG MVP | High | Keep adapters, retrieval, embeddings, and agents explicitly out of scope. |
| CRUD-only core misses platform intent | Medium | Require ingestion boundary plus audit event from day one. |
| Scaffold + migrations exceed review budget | Medium | Forecast chained PRs during tasks and split bootstrap from ingestion if needed. |

## Rollback Plan

Remove the new backend scaffold, migrations, and `knowledge-core-ingestion` delta artifacts. Since no production data exists yet, rollback is file-level deletion before archive.

## Dependencies

- Select concrete Go module layout and migration tool during design/tasks.
- PostgreSQL available for integration testing once code is introduced.

## Success Criteria

- [ ] A text ingestion command persists a source, knowledge object, link, and audit event transactionally.
- [ ] Specs define observable behavior for `knowledge-core-ingestion`.
- [ ] Tests or documented verification cover the ingestion path once tooling exists.
