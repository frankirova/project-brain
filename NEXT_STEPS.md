# Next Steps

Project Brain ya tiene el vertical slice principal funcionando: ingesta, búsqueda híbrida, detección de colisiones, validación humana por Telegram, MCP para agentes, persistencia de validaciones pendientes y retry/backfill de embeddings.

Este documento lista los próximos pasos recomendados en orden de impacto.

## 1. Verificar producción después de las migraciones

**Objetivo:** confirmar que las migraciones `0009` y `0010` funcionan en el entorno real.

- Aplicar migraciones en el VPS/base real.
- Validar que Telegram sigue guardando/descartando colisiones después de reiniciar el proceso.
- Validar que un fallo simulado de embeddings crea un job en `embedding_jobs` y que el worker lo drena después.
- Revisar logs de startup para confirmar:
  - sweep de `telegram_pending_validations`
  - arranque del embedding retry worker

**Verificación sugerida:**

```sh
go test -count=1 ./...
PROJECT_BRAIN_TEST_DATABASE_DSN="postgres://..." go test -count=1 -v ./internal/postgres
```

## 2. Agregar observabilidad mínima

**Objetivo:** dejar de depender solo de logs manuales.

Métricas prioritarias:

- cantidad de ingestas exitosas/fallidas
- cantidad de colisiones detectadas
- cantidad de validaciones Telegram pendientes/consumidas/expiradas
- tamaño de `embedding_jobs`
- retries de embeddings exitosos/fallidos
- latencia de búsqueda FTS/vector/híbrida

Una primera versión puede ser simple: endpoint `/v1/metrics` estilo Prometheus o counters loggeados periódicamente.

## 3. Mejorar el flujo humano de colisiones

**Objetivo:** pasar de una validación binaria mínima a una validación realmente útil.

Hoy el usuario puede:

- guardar igual
- descartar

Siguiente versión recomendada:

- mostrar claramente el mensaje nuevo y el conocimiento existente en conflicto
- agregar acción “revisar después”
- guardar decisiones humanas como audit events ricos
- preparar una revisión semanal de pendientes

## 4. Backfill operativo de embeddings existentes

**Objetivo:** asegurar que objetos históricos sin vector puedan recuperarse.

El retry queue cubre fallos nuevos. Falta un comando o tarea manual para detectar objetos existentes sin embedding y encolarlos.

Propuesta:

- comando interno o endpoint admin protegido
- query: knowledge objects sin fila compatible en `embeddings`
- insertar jobs en `embedding_jobs`
- reportar cantidad encolada

## 5. Endurecer modelo JSON persistido

**Objetivo:** evitar JSONB con claves PascalCase en estructuras anidadas.

Ya se agregaron JSON tags a `IngestTextRequest`. Queda revisar structs que viajan dentro de jobs o validaciones, especialmente:

- `domain.KnowledgeObject`
- `app.Collision`
- cualquier payload persistido en JSONB

Esto no bloquea el sistema porque el round-trip actual funciona, pero mejora inspección SQL y reduce fragilidad futura.

## 6. Decidir licencia y documentación pública

**Objetivo:** que el repo sea consumible por terceros sin ambigüedad.

- reemplazar `License: TBD` en `README.md`
- agregar `LICENSE` si corresponde
- decidir si el proyecto es privado, source-available u open source

## 7. Retomar el Research Agent

**Objetivo:** acercarse a la visión de fábrica de conocimiento automática.

Antes de implementar, definir:

- puerto `LLMClient`
- puerto `WebSearcher`
- formato de research report
- política de fuentes/citas
- idempotencia por topic

No arrancar con NATS ni multi-agent completo todavía. Primero un slice chico: pedir un tema, investigar, citar fuentes y guardar el resultado como knowledge object.

## No hacer todavía

- No meter NATS/EventBus hasta que duela de verdad.
- No cambiar ranking/collision thresholds sin datos nuevos.
- No implementar multi-agent completo antes de cerrar observabilidad y backfill.
- No borrar los artifacts locales ignorados sin decisión explícita.
