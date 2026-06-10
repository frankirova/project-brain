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
| `raw_inputs` | Todo lo que entra por Telegram (texto, links, transcripciones). No perder nunca el input crudo. | No existe |
| `knowledge_nodes` | Conceptos ya digeridos. Cada nodo tiene estado: `proposed`, `debating`, `validated`, `deprecated`. | Parcial (hoy es `knowledge_objects` con status libre) |
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
| 10 | `raw-inputs-table` | `raw-input-capture` | Low | Backlog |
| 11 | `knowledge-states` | `node-lifecycle` (máquina de estados proposed → debating → validated) | Medium | Backlog |
| 12 | `telegram-validation-ui` | `telegram-inline-keyboards` (botones de validar/debatir/descartar) | Low | Backlog |
| 13 | `sdd-documents` | `consolidated-sdd` (documento maestro actualizado por nodos validados) | High | Backlog |
| 14 | `human-loop-orchestrator` | `validation-workflow` (orquesta el bucle bot → humano → commit) | High | Backlog |

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
[✅] Fase 1: Foundation (5 cambios archived + sprint de calidad)
[→]  Fase 2: Hybrid RAG (próxima)
[ ]  Fase 3: Human-in-the-Loop Validation
[ ]  Fase 4: Multi-agent Platform
```

**Fase actual:** cerramos Fase 1 con 5 cambios archived + sprint de calidad (slog, rate limit, auth, CI, .gitattributes, fix del test de relations). Lo próximo es Fase 2 (embeddings + retrieval combinado).

---

## 🎯 Follow-ups identificados durante Fase 1

Cosas que aparecieron en verify/apply y que vale la pena trackear:

- [x] ~~Arreglar `internal/postgres/relation_repository_test.go` (warning del verify de `knowledge-relations` — 5 tests con `cannot scan NULL into *string`)~~ Resuelto en el sprint: evidence NOT NULL + migration 0004
- [ ] Decidir bounds de `confidence` (CHECK constraint en DB o validación solo en app)
- [ ] Decidir si agregar índice en `project_id` cuando exista la tabla `projects`
- [ ] Evaluar `confidence` negativo: ¿rechazar en app o confiar en el DB?
- [ ] Considerar 'spanish' o 'english' tsvector config por-objeto (hoy es 'simple' para bilingual MVP)

---

## 🔍 Deuda técnica conocida (de la auditoría)

Hallazgos del audit técnico post-Fase 1, en orden de prioridad para Fase 2/3.

### CRITICAL — bloquea Fase 2/3

**C1. Telegram bot usa `log` en vez de `slog` (Fase 3 blocker)**
`internal/telegram/handler.go:128,112` usa `fmt.Fprintf(log.Writer(), ...)` y `log.Printf` mientras el resto del código usa slog. Sin logging estructurado en el bot, no se puede tracear un chat_id → audit_event_id cuando agreguemos Inline Keyboards en Fase 3.
*Fix:* inyectar `*slog.Logger` en `telegram.Handler`, borrar el import de `log`.

**C2. `Status` es string libre sin enum (Fase 3 blocker)**
`KnowledgeObject.Status` se acepta como cualquier string. Para Fase 3 (proposed → debating → validated → deprecated) necesitamos un enum const + validación en `prepareIngestText` + CHECK constraint en DB.
*Fix:* migration 0005 con `CHECK (status IN ('active','proposed','debating','validated','deprecated'))`, agregar constantes en `domain/knowledge.go`.

**C3. No hay `Update` en `KnowledgeObjectRepository` (Fase 3 blocker)**
La interfaz solo tiene `Create`. Sin Update, los cambios de estado de Fase 3 requieren INSERT (destruyendo el audit chain).
*Fix:* agregar `Update(ctx, object)` a la interfaz, implementación con `UPDATE ... SET updated_at = now()` + nuevo `audit_events` row en la misma tx.

**C4. No hay `Retriever` port (Fase 2 blocker)**
No existe seam para retrieval. Agregar Fase 2 significa escribir SQL ad-hoc en un handler.
*Fix:* nuevo `internal/app/retrieval.go` con `Retriever` interface + `SearchQuery`/`SearchResult` types.

### HIGH — significant debt, will hurt soon

**H1. FTS no indexa `tags` y usa `'simple'` permanente**
Los tags son metadata de primera clase y deberían ser buscables. `simple` no hace stemming para ES ni EN. ROADMAP ya lo flageaba.
*Fix:* migration 0005: `ALTER COLUMN ... SET GENERATED ALWAYS AS (... || ' ' || array_to_string(tags, ' ')) STORED`. Config per-objeto de language viene después.

**H2. Duplicates no dejan audit trail**
`FindIngestionResultByIdentityKey` retorna duplicate sin escribir audit event. Sin trazabilidad de "cuántas veces el bot vio la misma idea".
*Fix:* escribir `AuditActionKnowledgeDuplicateDetected` o documentar el no-write-on-duplicate contract y surfacear métrica.

**H3. `FindIngestionResultByIdentityKey` query non-deterministic**
`ORDER BY ae.created_at DESC LIMIT 1` con un JOIN a `object_sources` sin LIMIT puede retornar el audit_event_id equivocado cuando un source tiene múltiples links. Se va a romper en Fase 2 cuando agreguemos chunks (one source → many objects).
*Fix:* subquery o restructurar a `(SELECT ... FROM sources WHERE ... LIMIT 1)`.

**H4. Auth middleware concatena JSON manualmente**
`internal/httpapi/auth/auth.go:52` arma JSON con string concat. Un `"` en el header rompe el JSON. Fase 3 va a copiar este patrón para JWT auth.
*Fix:* usar `json.Marshal` como en `httpapi/handler.go`.

**H5. Rate limit confía en `X-Forwarded-For` sin verificar**
Cualquier cliente puede spoofear el header y bypass del bucket. Detrás de un proxy es correcto, sin proxy es un DoS vector.
*Fix:* flag `TRUST_PROXY` en config; si está off, usar `r.RemoteAddr`.

**H6. Service layer no loguea nada**
IngestTextService y HTTP handler no emiten log de start/finish/validation. Con slog global es un agujero.
*Fix:* inyectar `*slog.Logger` en el service, log `workspace_id`, `content_bytes`, `duration_ms`, `duplicate`.

**H7. Commit error swallowed en `WithinIngestionTx`**
`tx.Commit` se retorna sin logging. Si falla (deadlock, connection drop), el caller recibe error opaco. Fase 3 necesita commits atómicos con audit en la fase de "Approve".
*Fix:* wrap commit error con slog + sentinel error tipado.

**H8. Telegram bot sin graceful shutdown**
`b.Start(ctx)` no se joinea. Updates in-flight se pueden perder mid-validation cuando llegue Fase 3.
*Fix:* wait group + bot.Close() si existe, timeout específico para el bot.

### MEDIUM — code quality, will hurt later

**M1. `computeIdentityKey` lowercaea workspaceID inconsistentemente**
La versión trimmed/lower se usa para identity_key pero la versión trimmed se persiste en `Source.WorkspaceID`. Divergencia silenciosa.
*Fix:* canonical form único (lowercase) en todo el flow, o documentar la divergencia.

**M2. `noopRepos`/`inMemoryUOW` viven en `main.go`**
Bloat del binario de producción, ensucia el composition root.
*Fix:* mover a `internal/app/inmem.go` o build tag.

**M3. `marshalMetadata` silently coerciona nil a `{}`**
Write de `Metadata: nil` es indistinguible de `Metadata: {}` en DB. Para Fase 3 ("cleared on validate" vs "not set") es importante.
*Fix:* preservar nil/empty en DB layer o documentar la pérdida.

**M4. `MaxBytesReader` error matched por string**
`if err.Error() == "http: request body too large"` se rompe en Go version bumps.
*Fix:* `errors.As(err, &maxBytesErr)` o chequear el tipo `*http.MaxBytesError`.

**M5. `json.NewEncoder(w).Encode(result)` ignora error**
Encode failures mid-write producen response truncado silencioso.
*Fix:* check + slog.

**M6. `audit_events` schema insuficiente para Fase 3**
Falta `before` (solo hay `after`), falta `reason`, falta `request_id`/`correlation_id`, target_type solo soporta knowledge_object via FK.
*Fix:* migration 0005: add `before JSONB`, `reason TEXT`, `request_id UUID`, polymorphic target (drop FK on target_id).

**M7. Rate limit sin cap superior**
`PROJECT_BRAIN_RATE_LIMIT_RPS=99999` se acepta. Sanity bound needed.
*Fix:* `if cfg.RateLimitRPS > 1000 { return error }`.

**M8. `AuditEvent` sin `Metadata` para context extra**
Solo tiene `After`, no puede llevar "user approved because...".
*Fix:* agregar `Metadata domain.Metadata`.

**M9. `repositories` struct no expone `Relations()` pero `DB` sí**
Asimetría confusa. `IngestionRepositories` interface no menciona relations.
*Fix:* documentar en docstring o agregar `Relations()` al UoW que retorna el standalone repo.

**M10. Two-step init pattern en Telegram handler**
`NewHandler(svc, nil)` + `SetBot(b)` es feo. Tests tienen que conocer el baile.
*Fix:* construir bot primero, pasarlo directo a `NewHandler(svc, b)`.

**M11. `ProcessUpdate` ignora `update.CallbackQuery`**
Para Fase 3 Inline Keyboards, los callbacks se dropean silenciosamente.
*Fix:* agregar el branch en el mismo change que introduzca Inline Keyboards (no diferir a Fase 3).

**M12. `os.Exit(1)` desde goroutine skips defers**
En `main.go:97` el `os.Exit` en el goroutine de ListenAndServe skips `dbCloser()`.
*Fix:* enviar error por channel, manejar en main.

**M13. `RelationRepository` mezclado en `ports.go`**
`ingestion ports` y `general ports` en el mismo archivo.
*Fix:* mover a `internal/app/relation_ports.go`.

### LOW — nice to have

**L1. `ErrValidation` sin campos estructurados**
Callers solo pueden checkar `errors.Is`, no extraer qué field falló. Fase 3 UI querrá highlight del campo.
*Fix:* typed error con `Field` y `Reason`.

**L2. Helpers `nullable*` triviales**
Aportan 6 líneas por call site sin valor semántico.
*Fix:* inline o remover.

**L3. `maxBodyBytes` hardcoded**
`1 << 20` debería ser configurable.
*Fix:* mover a `Config.IngestMaxBytes`.

**L4. Log level no override-able**
Hardcoded a `LevelDebug` en development.
*Fix:* env var `PROJECT_BRAIN_LOG_LEVEL`.

**L5. Checksum format inconsistente**
Content checksum es raw hex, identity_key es `sha256:hex`. Documentar.
*Fix:* normalizar o documentar.

**L6. `lastSeen` map redundante en rate limiter**
`bucket.last` ya tiene el timestamp.
*Fix:* drop el map, derive de `bucket.last`.

**L7. `Metadata` es `map[string]any`**
Schema fuzzy. Para embedding model + version en Fase 2, mejor typed sub-structs.
*Fix:* considerar `EmbeddingMetadata`, `IngestionMetadata`, o documentar keys permitidas.

**L8. `ShutdownSecs` env parser silencioso**
`PROJECT_BRAIN_SHUTDOWN_SECS=abc` cae al default sin warning.
*Fix:* warning o error.

**L9. In-memory mode emite `Warn` en producción**
Sin DB en prod es configuration error, no warning.
*Fix:* si `Environment == "production"`, log Error + exit.

---

## 📚 Referencias

- `PROJECT_BRAIN.md` — visión completa del producto, decisiones macro, arquitectura
- `openspec/specs/` — source of truth de las capabilities implementadas
- `openspec/changes/archive/` — audit trail de cada cambio (proposal + design + tasks + verify)
- `GO_CODE_WALKTHROUGH.md` — paseo por el código Go (intro para nuevos devs)
- `README.md` — cómo correr el proyecto
