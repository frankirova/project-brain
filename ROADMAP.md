# Roadmap — Project Brain

Estado vivo del proyecto. Fases completadas colapsadas; solo queda lo pendiente.

---

## 📊 Estado

```
[✅] Fase 1: Foundation        — ingest, FTS, relaciones, auditoría, idempotencia
[✅] Fase 2: Hybrid RAG         — embeddings, vector search, composite retrieval, collision detection
[✅] Fase 3: Human-in-the-Loop  — raw inputs, lifecycle, backlog, debate, Telegram UI, consolidated SDD
[ ]  Fase 4: Multi-agent Platform
[✅] Hardening: production-readiness (ver abajo)
```

**Sistema actual**: ingest vía HTTP + Telegram → collision detection → human backlog → validar/debatir/descartar vía inline keyboards → lifecycle auditado en Postgres.

---

## ✅ Hardening batch (producción) — cerrado

Change-16 (PR1→PR5) cerró el batch original de hallazgos CRITICAL/HIGH y los MEDIUMs de tooling. Estado detallado en **Deuda técnica abierta** más abajo.

**Pendientes residuales** (no bloquean Fase 4, entran como follow-ups):
- `cmd/api` composition root: tests actuales son backstop estructural (`main_test.go`, `auth_invariance_test.go`). Falta suite de wiring completa.
- Postgres integration tests: DSN-gated, mantener cuando entre Fase 4.
- Envelope de error uniforme (RFC7807) en HTTP.
- Security headers globales en HTTP middleware.
- Refactor progresivo de `cmd/api/main.go` para partir el composition root (próximo change).

---

## 🔮 Después — Fase 4: Multi-agent Platform

Una vez vacío el hardening batch.

| Agente | Responsabilidad |
|--------|-----------------|
| **Orchestrator** | Entiende intención, elige modo, crea plan, delega |
| **Knowledge Processor** | Clasifica, extrae entidades, detecta decisiones/tareas/ideas |
| **Research** | Investiga temas, compara alternativas, sintetiza |
| **Architect** | Genera arquitectura, SDD, ERD, define APIs |
| **Coder** | Analiza repos, propone refactors, genera docs |
| **Retrieval** | Combina semántica + keyword + estructurada + grafo |

Cambios estimados: `event-driven-pipeline` (NATS), `agent-framework`, `prompt-registry`, `evaluation-suite`.

---

## 🎯 Follow-ups pendientes

- [ ] Decidir bounds de `confidence` (CHECK constraint en DB o validación solo en app)
- [ ] Decidir si agregar índice en `project_id` cuando exista la tabla `projects`
- [ ] Evaluar `confidence` negativo: ¿rechazar en app o confiar en el DB?
- [ ] Considerar `'spanish'` o `'english'` tsvector config por-objeto (hoy es `'simple'` para bilingual MVP)

---

## 🔍 Deuda técnica abierta

| ID | Hallazgo | Estado |
|---|---|---|
| H1 | FTS per-row language | 🟡 Diferido: tags incluidos, per-row language pospuesto |
| M6 | `audit_events` wire en código de aplicación | ✅ Resuelto: `RequestID` propagado desde `X-Request-ID` header en ingest path |
| M11 | `ProcessUpdate` callback handler | ✅ Resuelto en Fase 3 (change 15) |
| M13 | `RelationRepository` mezclado en `ports.go` | ⏳ Bajo valor, skip |
| L2 | Helpers `nullable*` triviales | ⏳ Bajo valor, skip |
| L5 | Checksum format inconsistente | ⏳ Documentar (no normalizar) |
| H2 | Auth fail-open si falta token | ✅ Resuelto (change 16, PR1: `enforceProductionAuth` fail-closed en `cmd/api/auth_invariance.go`) |
| H3 | HTTP server sin timeouts | ✅ Resuelto (change 16, PR2: timeouts configurables en `cmd/api/main.go`) |
| H4 | SDD document update concurrente | ✅ Resuelto (change 16, PR3: row lock + `WithinSddDocumentTx`) |
| H5 | Health sin readiness | ✅ Resuelto (change 16, PR4: `/v1/liveness` + `/v1/readiness` con DB ping) |
| M1 | RFC 7807 problem details | ✅ Resuelto (PR #27: `internal/httpapi/problem` + `application/problem+json` opt-in via `Accept`) |
| M2 | OWASP security headers | ✅ Resuelto (PR #27: `internal/httpapi/security` + 6 headers baseline + HSTS condicional) |
| M3 | Composition root split | ✅ Resuelto (change 19: `internal/runtime` con `BuildServices`/`BuildServer`/`BuildTelegramBot`/`RunShutdown`) |

---

## 📚 Referencias

- `PROJECT_BRAIN.md` — visión completa del producto, decisiones macro, arquitectura
- `openspec/specs/` — source of truth de las capabilities implementadas
- `openspec/changes/archive/` — audit trail de cada cambio
- `README.md` — cómo correr el proyecto
