# Paradigm Knowledge OS — Research Backlog

Puntos pendientes para continuar el proyecto de manera profesional.
Origen: `openspec/changes/paradigm-knowledge-os/{exploration.md, proposal.md, design.md §11, tasks.md}`.

---

## 1. Decisiones de diseño abiertas (`design.md §11`)

### 1.1 `freshness` derivado — superficie en read API
- **Duda**: ¿se expone como campo en la respuesta del read API junto a `confidence` y `computed_confidence`, o queda solo en audit?
- **Default recomendado**: exponerlo en la respuesta, al lado de `confidence` y `computed_confidence`.
- **Cierra con**: change #4 `freshness-and-owner`.

### 1.2 Semántica NACK de NATS
- **Duda**: ¿JetStream at-least-once? ¿backoff exponencial? ¿cap 5? ¿dead-letter `{subject}.dlq`?
- **Default recomendado**: JetStream at-least-once, backoff exponencial, cap 5, dead-letter `{subject}.dlq`.
- **Cierra con**: change #8 `event-bus-nats`.

### 1.3 Transactional outbox para NATS publish
- **Duda**: hoy acquisition escribe `raw_inputs + audit_events` y publica aparte (gap write-then-publish). ¿Outbox (`pending_events` table + drainer) sí/no?
- **Default recomendado**: sí, outbox.
- **Cierra con**: change #8 `event-bus-nats`.

### 1.4 Forma exacta del test del 4-write contract rename
- **Duda**: el change #5 renombra `TestIngestDoesNotRequireDeferredExternalCapabilities` → `TestIngestHasNoDeferredExternalDependencies`. ¿Qué asserts lleva? ¿"no LLM", "no NATS", "no embedder", todos?
- **Cierra con**: change #5 `raw-inputs-inbox`.

---

## 2. Risks HIGH que requieren atención

### 2.1 `knowledge-compiler` (#7) — HIGH, multi-semana
- `agreement_score` (input a `computed_confidence`) hoy es externo (RelationBuilder / Validator).
- Falta formalizar una definición derivable de DB.
- **Cierra con**: follow-up chico después de que cierre el change #12.
- Documentado en `design.md §9`.

### 2.2 `event-bus-nats` (#8) — riesgo de integración
- NATS client + pgx connection pool: las subscriptions NO deben bloquear shutdown.
- Acumula las decisiones 1.2 (NACK) y 1.3 (outbox).

### 2.3 `agents-shared-brain` (#9) — HIGH, 2 semanas
- La open exploration de `research-agent` recomienda llamar `IngestTextService` directo. Esa recomendación se rechaza acá.
- Hay que redibujar el límite del agente y unificar todo detrás de `Write(ctx, in) (ObjectID, error)`.

### 2.4 `relations-bidirectional-and-freshness` (#12) — MEDIUM
- Columnas nuevas (`weight`, `valid_from`, `valid_until`) en tabla caliente.
- `GraphExpander` retriever se integra con el Hybrid Retrieval Engine del change #10.
- Tras landear, recién se puede formalizar `agreement_score` (ADR-002).

---

## 3. Brechas detectadas en la exploration (`exploration.md` §"Gap Analysis")

| Gap                                                   | Estado actual                                       | Change que lo cierra |
|-------------------------------------------------------|-----------------------------------------------------|----------------------|
| Lifecycle 7-state (RAW→...→DEPRECATED)                | Parcialmente migrado                                | #2                   |
| Mandatory metadata (id, type, status, version, ...)   | No enforced                                         | #1 + #4 + #7         |
| Versionado (never overwrite)                          | `UpdateStatus` muta in-place                        | #3                   |
| Typed relations (knowledge graph)                     | ALREADY THERE (14 tipos)                            | —                    |
| Confidence scoring formula                            | Campo libre, sin fórmula                            | #7                   |
| Freshness / `next_review_at` / `stale_after`          | No existen                                          | #4                   |
| Event-driven pipeline (NATS)                          | Sin cliente, sin publisher port, sin subscriber     | #8                   |
| Inbox pattern                                         | No hay `raw_inputs`, no hay `knowledge_inbox`       | #5                   |
| Knowledge Compiler como componente                    | No existe `internal/compiler/`                      | #7                   |
| Agentes especializados con un brain compartido        | Parcialmente (tabla compartida)                     | #9                   |
| HNSW index sobre `embeddings.embedding`               | Faltante (escaneo secuencial)                       | #11                  |

---

## 4. Orden sugerido de ejecución

### 4.1 Quick wins independientes (sin dependencias, ½ día cada uno)
1. #11 `hnsw-embedding-index` — fix de performance real, `CONCURRENTLY` obligatorio.
2. #13 `audit-duplicate-write` — constante ya existe en `domain/knowledge.go:109`.
3. #15 `rodoc-paradigm-update` — solo docs.

### 4.2 Base fundacional (rompe tests legacy, primero)
4. #1 `workspace-id-uuid-migration` — `workspace_id` TEXT → UUID.
5. #2 `lifecycle-states-migration` — 5-10 tests hardcodean statuses.

### 4.3 Core del dominio
6. #3 `versioning-and-versions-table`.
7. #4 `freshness-and-owner` — toma decisión 1.1.
8. #6 `lifecycle-events-log`.
9. #5 `raw-inputs-inbox` — toma decisión 1.4.

### 4.4 Pipeline + bus
10. #7 `knowledge-compiler` — pieza central, multi-semana.
11. #8 `event-bus-nats` — toma decisiones 1.2 y 1.3.
12. #10 `hybrid-retrieval-wiring` — wiring mecánico.
13. #12 `relations-bidirectional-and-freshness` — habilita el follow-up de `agreement_score`.

### 4.5 Capa de agentes + UI
14. #9 `agents-shared-brain` — refactor de frontera de agentes.
15. #14 `telegram-validation-ui` — human-in-the-loop.

---

## 5. Cómo lo vamos a trabajar

- **Un punto a la vez**. Vos elegís cuál encarar primero.
- Por cada punto: exploro / decidimos / lo dejo por escrito → commit.
- Si surge una decisión nueva, la agrego a la sección 1 de este archivo.

---

## Estado

- [x] 1.1 `freshness` en read API — **DECIDIDO 2026-06-10**: opción A (top-level junto a `confidence` y `computed_confidence`). Ver `research/01-1-confidence-freshness-model.md`.
- [x] 1.2 Semántica NACK de NATS — **DECIDIDO 2026-06-10**: default del backlog + 5 refinaciones (at-least-once + idempotencia, `BackOff` server-side, `MaxDeliver = 5/3` por worker, DLQ advisory-driven a `{subject}.dlq`, `Term()` para poison). Ver `research/01-2-nats-nack-semantics.md`.
- [x] 1.3 Transactional outbox — **DECIDIDO 2026-06-10**: opción A con `pending_events` + drainer polling `FOR UPDATE SKIP LOCKED` cada 100ms, mark-don't-delete con GC a 7 días, multi-drainer safe. Ver `research/01-3-transactional-outbox.md`.
- [x] 1.4 Forma del test rename (change #5) — **DECIDIDO 2026-06-10**: test canónico con 5 capas de assertion (panicking fakes en LLM/NATS/Embedder, 3 escrituras raw_input+audit_event+pending_events, latencia < 100ms, goleak) + 3 tests hermanos granulares con call recorders. Refactor mínimo del constructor a `IngestTextDeps`. Ver `research/01-4-4-write-contract-test.md`.
- [ ] 2.1 `knowledge-compiler` — acuerdo sobre `agreement_score` — en análisis
- [x] 2.2 `event-bus-nats` — **DECIDIDO 2026-06-10**: JetStream + outbox writer + drainer (`cmd/drainer`) + DLQ-watcher (`cmd/dlq-watcher`) + shutdown coordination (NATS drena antes de pgx). 1 semana, 2 procesos separados. Ver `research/02-2-event-bus-nats.md`.
- [ ] 2.3 `agents-shared-brain` — refactor de frontera — en análisis
- [x] 2.4 `relations-bidirectional-and-freshness` — **DECIDIDO 2026-06-10**: agregar 3 columnas (`weight`, `valid_from`, `valid_until`), nuevo `GraphExpander` retriever con CTE bidireccional, 3 índices (`CONCURRENTLY`), `agreement_score` queda como follow-up (½ día, ADR-002). Ver `research/02-4-relations-bidirectional-freshness.md`.
- [ ] 3. Brechas de `exploration.md` — todas mapeadas a un change
- [ ] 4. Quick wins ejecutados (#11, #13, #15) — #11 y #13 listos (analizados), #15 en análisis
