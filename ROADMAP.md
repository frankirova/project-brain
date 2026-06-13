# Roadmap — Project Brain

Estado vivo del proyecto. Fases completadas colapsadas; solo queda lo pendiente.

---

## 📊 Estado

```
[✅] Fase 1: Foundation        — ingest, FTS, relaciones, auditoría, idempotencia
[✅] Fase 2: Hybrid RAG         — embeddings, vector search, composite retrieval, collision detection
[✅] Fase 3: Human-in-the-Loop  — raw inputs, lifecycle, backlog, debate, Telegram UI, consolidated SDD
[ ]  Fase 4: Multi-agent Platform
```

**Sistema actual**: ingest vía HTTP + Telegram → collision detection → human backlog → validar/debatir/descartar vía inline keyboards → lifecycle auditado en Postgres.

---

## ✅ Último entregado — Change 13: `sdd-documents`

**Capacidad**: `consolidated-sdd` — documento maestro que se actualiza cuando knowledge objects pasan a `validated`.

**Estado**: mergeado en `main` vía PR #13.

---

## 🔜 Próximo — Fase 4: Multi-agent Platform

**Primer candidato**: `event-driven-pipeline` — base NATS para coordinar agentes y procesamiento asíncrono.

**Esfuerzo**: High — requiere SDD completo.

---

## 🔮 Fase 4 — Multi-agent Platform

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

---

## 📚 Referencias

- `PROJECT_BRAIN.md` — visión completa del producto, decisiones macro, arquitectura
- `openspec/specs/` — source of truth de las capabilities implementadas
- `openspec/changes/archive/` — audit trail de cada cambio
- `README.md` — cómo correr el proyecto
