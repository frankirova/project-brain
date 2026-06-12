# Roadmap — Project Brain

Estado vivo del proyecto. Lo que está hecho, lo que viene, y las decisiones pendientes.

---

## ✅ Fase 1 — Foundation (completa)

Esta fase entrega el núcleo mínimo viable: una plataforma de conocimiento auditable que acepta texto, lo persiste transaccionalmente, y permite consultarlo.

### Cambios entregados

| # | Cambio | Capacidad | Estado |
|---|--------|-----------|--------|
| 1 | `project-brain-mvp` | `knowledge-core-ingestion` | ✅ Archived |
| 2 | `ingest-text-http-api` | `http-ingest-api` | ✅ Archived |
| 3 | `telegram-bot-ingestion` | `telegram-bot-adapter` | ✅ Archived |
| 4 | `knowledge-relations` | `knowledge-relations` | ✅ Archived |
| 5 | `knowledge-pipeline` | `knowledge-core-ingestion` (delta: FTS + §10.1) | ✅ Archived |

### Lo que funciona hoy

- **Ingestar texto** vía HTTP (`POST /v1/ingest-text`) o Telegram (mensajes directos)
- **Búsqueda full-text** sobre title + summary + content (PostgreSQL FTS, `'simple'` config)
- **Relaciones tipadas** entre knowledge objects (14 tipos: relates_to, depends_on, contradicts, etc.)
- **Auditoría** de cada mutación (`audit_events` con actor, action, target)
- **Idempotencia** basada en `identity_key` (sha256 de workspace + source + content checksum)
- **Contenedores** con `docker compose up` (Postgres + API en un comando)

### Decisiones arquitecturales que tomamos (y por qué)

**PostgreSQL-first, no Neo4j ni vector DB separado.**
Una sola base reduce complejidad operativa. Postgres soporta JSONB, FTS, pgvector, y transacciones. El grafo lo modelamos con tablas relacionales; es suficiente para MVP. Si el volumen crece o las queries de grafo se vuelven el cuello de botella, evaluamos Neo4j en una fase posterior.

**Sin embeddings todavía.**
FTS alcanza para keyword search. Embeddings con pgvector vienen en una fase posterior (cambio `embeddings-pgvector`) — agregan un worker async, modelo, costos de API. Queríamos cerrar la fundación limpia antes de meter complejidad operativa.

**Ingest sincrónico, no event-driven.**
Para MVP, la latencia de un ingest (decenas de milisegundos contra Postgres) es perfectamente tolerable. NATS + workers async es Phase 5+ del roadmap original. Lo agregamos cuando el volumen o los pasos múltiples lo justifiquen.

**Standalone repositories, no Unit-of-Work para todo.**
`RelationRepository` vive fuera del `IngestionUnitOfWork`. La razón: no queríamos acoplar "crear relación" al flujo de ingest. Las relaciones las crean agentes futuros, comandos `/research`, análisis post-ingest. Si más adelante necesitamos "ingest + relación atómica", agregamos un accessor al UoW sin romper nada.

**Idempotencia por content + workspace, no UUIDs aleatorios.**
`identity_key = sha256(workspace_id + source_type + locator + content_checksum)`. Reintentos seguros. Si dos fuentes distintas mandan el mismo texto, se crean registros separados (es lo correcto: la fuente importa). Si la misma fuente manda el mismo texto dos veces, retorna el ID existente (es lo correcto: no duplicamos).

**4 escrituras por ingest, contrato sagrado.**
Source + KnowledgeObject + ObjectSource + AuditEvent. El test `TestIngestDoesNotRequireDeferredExternalCapabilities` codifica este contrato. El FTS column es generada por Postgres (no por la app) precisamente para no romperlo.

---

## 🔄 Fase 2 — Hybrid RAG (siguiente)

El objetivo: que el bot no solo guarde conocimiento, sino que **detecte colisiones** con conocimiento existente y proponga acciones.

### Cambios planeados

| # | Cambio | Capacidad | Esfuerzo | Estado |
|---|--------|-----------|----------|--------|
| 6 | `embeddings-pgvector` | `vector-similarity-search` | Medium | Backlog |
| 7 | `fts-search-api` | `keyword-search` | Low | Backlog |
| 8 | `hybrid-retrieval` | `hybrid-rag` (combina FTS + vector + structured) | High | Backlog |
| 9 | `relation-traversal` | `graph-expansion` (queries por relaciones) | Medium | Backlog |

### Lo que habilita

- "Traeme todo lo que hablamos sobre Redis" → búsqueda keyword (FTS)
- "¿Qué alternativas a Redis evaluamos?" → búsqueda semántica (embeddings)
- "¿Qué decisiones tomamos para el CRM?" → búsqueda estructurada (filtros por tipo/proyecto)
- "¿Cuándo descartamos Prometheus?" → traversal de grafo (relación `supersedes` o `contradicts`)

---

## 🎯 Fase 3 — Human-in-the-Loop Validation (la que charlamos arriba)

Esta es la fase donde Project Brain deja de ser "lugar para tirar notas" y se convierte en **plataforma de conocimiento con workflow**. El bot no solo guarda: propone, cuestiona, y espera validación humana.

### La visión: el "Embudo de Validación"

```
1. Estado: "Engrama en Crudo" (Draft)
   Bot recibe mensaje/audio/link → transcribe → extrae entidad central
   → guarda en raw_inputs con estado "draft"
   Todavía no afecta la arquitectura de proyectos

2. Fase de Colisión y Contexto (RAG Activo)
   Antes de promover a "knowledge", el bot busca en pgvector
   si este input afecta algo que ya existe
   Ejemplo: tío propone NoSQL → bot recupera SDD actual que dice Postgres
   → bot prepara alerta de conflicto o actualización

3. Bucle de Validación (Human-in-the-Loop)
   El bot usa Inline Keyboards de Telegram para pedir validación explícita
   "Procesé la idea sobre el nuevo módulo. Choca con el diseño actual.
    ¿Qué hacemos?"
       [ ⚖️ Debatir ahora ]
       [ 📌 Guardar para revisión semanal ]
       [ 🗑️ Descartar ambas ]

4. Estado: "Conocimiento Validado" (Commit)
   Cuando un humano aprieta un botón, el sistema ejecuta el cambio
   - Si se aprueba, el LLM reescribe la sección afectada del SDD
   - Se guardan los nuevos vectores
   - Próxima consulta: la "verdad" validada, no la idea vieja
```

### Modelo de datos propuesto

**3 tablas conceptuales, no 1:**

| Tabla | Propósito | Estado |
|-------|-----------|--------|
| `raw_inputs` | Todo lo que entra por Telegram (texto, links, transcripciones). No perder nunca el input crudo. | Existe (`raw-inputs-table` archived) |
| `knowledge_nodes` | Conceptos ya digeridos. Cada nodo tiene estado: `proposed`, `debating`, `validated`, `deprecated`. | Parcial: `knowledge_objects` soporta validación/rechazo (`proposed → validated/deprecated`); `debating` pendiente |
| `sdd_documents` | Documento maestro consolidado. Se actualiza cuando nodos pasan a `validated`. | No existe |

### Decisión pendiente: cuarentena vs prioridad en conflictos

Cuando dos personas mandan ideas contradictorias sobre el mismo proyecto, ¿qué hace el bot?

| Opción | Pro | Contra |
|--------|-----|--------|
| **Cuarentena hasta acuerdo mutuo** | Nadie "gana" sin共识, calidad de decisión | Items colgados semanas, sistema lleno de pendientes |
| **Prioridad al que inició el hilo** | Decisión rápida, clara | Asimetría peligrosa (¿y si el junior contesta primero dormido?) |
| **El bot no decide, presenta el conflicto con contexto** | El bot es árbitro, no juez. Ambos debaten con datos. | Más trabajo de diseño de UI, requiere más tiempo humano |

**Recomendación actual:** El bot no decide. Presenta el conflicto con contexto (RAG retrieval + las dos ideas lado a lado) y deja que ambos lo debatan. La UI propone acciones, no impone outcomes.

### Cambios estimados para esta fase

| # | Cambio | Capacidad | Esfuerzo | Estado |
|---|--------|-----------|----------|--------|
| 10 | `raw-inputs-table` | `raw-input-capture` | Low | ✅ Archived |
| 12 | `knowledge-states` | `node-lifecycle` (`proposed → validated`, `proposed → deprecated`) | Medium | ✅ Archived |
| 13 | `sdd-documents` | `consolidated-sdd` (documento maestro actualizado por nodos validados) | High | Backlog |
| 14 | `human-loop-orchestrator` | `validation-workflow` (diseña `debating` y orquesta bot → humano → commit) | High | Backlog |
| 15 | `telegram-validation-ui` | `telegram-inline-keyboards` (botones de validar/debatir/descartar) | Low | Backlog |

### Por qué NO empezar por acá todavía

Antes de construir el bucle de validación, necesitamos la retrieval funcionando. Si el bot no puede **encontrar** el conflicto, no puede presentarlo. Fases 1 y 2 son prerrequisito.

El orden importa:
1. ✅ Foundation (ingest + relaciones + FTS)
2. → Hybrid RAG (embeddings + retrieval combinado)
3. → Human-in-the-Loop (el bucle de validación)
4. → Dashboard web y graph explorer

---

## 🔮 Fase 4 — Multi-agent Platform (futuro)

Esto es lo que define `PROJECT_BRAIN.md` sección 8. Los agentes especializados que convierten Project Brain en una plataforma real, no solo una base de conocimiento.

### Agentes planeados

| Agente | Responsabilidad | Salidas |
|--------|-----------------|---------|
| **Orchestrator** | Entiende intención, elige modo, crea plan, delega | Plan + composición de resultados |
| **Knowledge Processor** | Clasifica, extrae entidades, detecta decisiones/tareas/ideas, decide persistencia | Knowledge nodes con metadata enriquecida |
| **Research** | Investiga temas, compara alternativas, sintetiza, vincula con existente | `Research`, `Benchmark`, `Source`, `DecisionCandidate`, `Artifact` |
| **Architect** | Genera arquitectura, SDD, ERD, define APIs, evalúa trade-offs | `Architecture`, `Decision`, `Requirement`, `Roadmap`, `Artifact` |
| **Coder** | Analiza repos, propone refactors, genera docs, crea patches bajo control | `CodeAnalysis`, `Issue`, `Task`, `Snippet`, `Artifact` |
| **Founder** | Analiza negocio, pricing, competencia, mercado, MVP, go-to-market | `BusinessModel`, `Benchmark`, `Roadmap`, `Idea`, `Artifact` |
| **Documentation** | Transforma conocimiento en documentos (READMEs, ADRs, SDDs) | Reportes, specs, diagramas |
| **Retrieval** | Decide cómo buscar, combina semántica + keyword + estructurada + grafo | Respuestas con citas y fuentes |

### Cambios estimados (alto nivel)

- `event-driven-pipeline` (NATS + workers) — Phase 5+ del roadmap original
- `agent-framework` (Orchestrator + delegación)
- `prompt-registry` (prompts versionados por agente)
- `evaluation-suite` (métricas de calidad por agente)

---

## 📊 Estado del roadmap

```
[✅] Fase 1: Foundation (5 cambios archived + 2 sprints de calidad)
[🚀] Fase 2: Hybrid RAG (en curso)
[ ]  Fase 3: Human-in-the-Loop Validation
[ ]  Fase 4: Multi-agent Platform
```

**Fase 2 progreso:**
- ✅ `Retriever` port (C4 cerrado)
- ✅ `FTSRetriever` + endpoint `GET /v1/search`
- ✅ Embeddings con pgvector (migration 0007, EmbeddingRepo, vectorRetriever)
- ✅ `CompositeRetriever` con Reciprocal Rank Fusion (merge FTS + vector)
- ✅ Hydration del composite (ObjectHydrator interface + FTSRetriever.FindByID)
- ✅ `/v1/objects/{id}` endpoint
- ✅ Wire del composite en main.go (Gemini key → hybrid; no key → FTS-only)
- ✅ Integration tests del `vectorRetriever` + `EmbeddingRepo` contra Postgres real (`embeddings_integration_test.go`: 3 tests env-gated)

**Fase 1 cerrado** con 5 cambios archived + **dos sprints de calidad** (slog, rate limit, auth, CI, .gitattributes, fix del test de relations + 25 commits del audit post-Fase 1).

La fundación está lista. Los blockers arquitecturales (status enum, Update method, FTS con tags, queries determinísticas, XFF spoofing, graceful shutdown, etc.) están resueltos.

---

## 🎯 Follow-ups identificados durante Fase 1

- [x] ~~Arreglar `internal/postgres/relation_repository_test.go`~~ (sprint 1: evidence NOT NULL + migration 0004)
- [x] ~~FTS cubre tags y usa pesos~~ (migration 0006: title A, summary B, tags B, content C)
- [x] ~~Audit events enriquecidos~~ (migration 0005: before, reason, request_id, polymorphic target)
- [x] ~~XFF spoofing arreglado~~ (TrustProxy flag, default false)
- [ ] Decidir bounds de `confidence` (CHECK constraint en DB o validación solo en app)
- [ ] Decidir si agregar índice en `project_id` cuando exista la tabla `projects`
- [ ] Evaluar `confidence` negativo: ¿rechazar en app o confiar en el DB?
- [ ] Considerar 'spanish' o 'english' tsvector config por-objeto (hoy es 'simple' para bilingual MVP)

---

## 🔍 Deuda técnica conocida (post-audit)

Resultados del audit post-Fase 1 con estado actual.

### CRITICAL — bloquea Fase 2/3

| ID | Hallazgo | Estado |
|---|---|---|
| C1 | Telegram usaba `log` en vez de `slog` | ✅ Resuelto: `*slog.Logger` inyectado |
| C2 | `Status` era string libre sin enum | ✅ Resuelto: enum const + CHECK constraint en migration 0005 |
| C3 | No había `Update` en `KnowledgeObjectRepository` | ✅ Resuelto: `UpdateStatus` agregado |
| C4 | No había `Retriever` port | ✅ Resuelto: `Retriever` port + `FTSRetriever` + `CompositeRetriever` |

### HIGH — significant debt

| ID | Hallazgo | Estado |
|---|---|---|
| H1 | FTS no indexaba `tags`, `'simple'` permanente | ✅ Parcial: tags incluidos con pesos A/B/B/C en migration 0006; per-row language sigue diferido |
| H2 | Duplicates no dejan audit trail | ⏳ Pendiente: escribir `AuditActionKnowledgeDuplicateDetected` o documentar contract |
| H3 | `FindIngestionResultByIdentityKey` query non-deterministic | ✅ Resuelto: subquery con `ORDER BY id LIMIT 1` |
| H4 | Auth middleware concatenaba JSON manualmente | ✅ Resuelto: `json.Marshal` con struct tipado |
| H5 | Rate limit confiaba en `X-Forwarded-For` sin verificar | ✅ Resuelto: `PROJECT_BRAIN_TRUST_PROXY` flag, default false |
| H6 | Service layer no loguea nada | ✅ Resuelto: `*slog.Logger` en service + handler con elapsed/workspace_id |
| H7 | Commit error swallowed en `WithinIngestionTx` | ✅ Resuelto: `OpenWithLogger` + commit error loggeado loudly |
| H8 | Telegram bot sin graceful shutdown | ✅ Resuelto: `sync.WaitGroup` + timeout en shutdown |

### MEDIUM — code quality

| ID | Hallazgo | Estado |
|---|---|---|
| M1 | `computeIdentityKey` lowercaea workspaceID inconsistentemente | ✅ Resuelto: lowercase en `prepareIngestText`, persistido igual |
| M2 | `noopRepos`/`inMemoryUOW` en `main.go` | ✅ Resuelto: movido a `internal/app/inmem` |
| M3 | `marshalMetadata` coerciona nil a `{}` | ✅ Resuelto: nil → SQL NULL, empty → `'{}'` |
| M4 | `MaxBytesReader` matched por string | ✅ Resuelto: `errors.As(err, &maxBytesErr)` |
| M5 | `json.NewEncoder.Encode` ignora error | ✅ Resuelto: 3 sites con log en fallo |
| M6 | `audit_events` schema insuficiente para Fase 3 | 🟡 Schema listo (migration 0005), wire pendiente en código de aplicación |
| M7 | Rate limit sin cap superior | ✅ Resuelto: cap 1000 RPS / 10000 burst |
| M8 | `AuditEvent` sin `Metadata` para context extra | ⏳ Pendiente: agregar `Metadata domain.Metadata` al struct |
| M9 | `repositories` no expone `Relations()` pero `DB` sí | ✅ Resuelto: doc comment explica la asimetría |
| M10 | Two-step init en Telegram | ✅ Resuelto: `SetBot` removido, bot se inyecta lazy via `DefaultHandler` |
| M11 | `ProcessUpdate` ignora `update.CallbackQuery` | 🟡 Stub agregado (log + ack), handler real con Fase 3 |
| M12 | `os.Exit` desde goroutine skips defers | ✅ Resuelto: `cancel()` en lugar de `os.Exit` |
| M13 | `RelationRepository` mezclado en `ports.go` | ⏳ Pendiente: bajo valor, skip |

### LOW — polish

| ID | Hallazgo | Estado |
|---|---|---|
| L1 | `ErrValidation` sin campos estructurados | ✅ Resuelto: `FieldError` con `errors.As` |
| L2 | Helpers `nullable*` triviales | ⏳ Pendiente: skip (bajo valor) |
| L3 | `maxBodyBytes` hardcoded | ✅ Resuelto: `PROJECT_BRAIN_INGEST_MAX_BYTES` env var |
| L4 | Log level no override-able | ✅ Resuelto: `PROJECT_BRAIN_LOG_LEVEL` env var |
| L5 | Checksum format inconsistente | ⏳ Pendiente: documentar (no normalizar) |
| L6 | `lastSeen` map redundante en rate limiter | ✅ Resuelto: derive de `bucket.last` |
| L7 | `Metadata` es `map[string]any` | ✅ Resuelto: doc comment con reserved keys |
| L8 | `ShutdownSecs` env parser silencioso | ✅ Resuelto: warning a stderr |
| L9 | In-memory mode en producción | ✅ Resuelto: `os.Exit(1)` si env=production sin DSN |

---

## 📈 Métricas del sprint

| Métrica | Valor |
|---------|-------|
| Commits entregados | 25 |
| Findings resueltos | 26/34 (76%) |
| Migraciones nuevas | 0005, 0006 |
| Tests rotos arreglados | 11 (relation_repository_test) |
| Build status | ✅ limpio |
| Tests status | ✅ 7/8 packages verde (internal/app bloqueado por Windows Defender falso positivo) |

---

## 📚 Referencias

- `PROJECT_BRAIN.md` — visión completa del producto, decisiones macro, arquitectura
- `openspec/specs/` — source of truth de las capabilities implementadas
- `openspec/changes/archive/` — audit trail de cada cambio (proposal + design + tasks + verify)
- `README.md` — cómo correr el proyecto
