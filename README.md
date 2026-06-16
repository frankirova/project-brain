# Project Brain

Plataforma de conocimiento auditable. Captura texto desde HTTP o Telegram, lo persiste con trazabilidad completa, permite buscarlo por keyword o (próximamente) por similitud semántica, y conecta conceptos vía relaciones tipadas.

No es un chatbot con memoria frágil. Es un **núcleo de conocimiento estructurado** sobre PostgreSQL.

---

## Quickstart

```sh
# Levantar todo (Postgres + API)
docker compose up

# O solo el server local con Postgres del compose
PROJECT_BRAIN_DATABASE_DSN="postgres://postgres:postgres@127.0.0.1:5433/project_brain?sslmode=disable" \
  go run ./cmd/api
```

El server queda en `http://localhost:8050`. Postgres en `localhost:5433`.

### Endpoints HTTP

```sh
# Health check (público, no requiere auth)
curl http://localhost:8050/v1/health

# Ingestar texto (requiere bearer token si PROJECT_BRAIN_AUTH_TOKEN está set)
curl -X POST http://localhost:8050/v1/ingest-text \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $PROJECT_BRAIN_AUTH_TOKEN" \
  -d '{
    "workspace_id": "mi-workspace",
    "content": "Project Brain es una plataforma de conocimiento",
    "source": {"type": "manual", "identifier": "doc-1"},
    "object": {"type": "document", "metadata": {"title": "Intro"}}
  }'
```

**Endpoints disponibles:**

| Método | Path | Auth | Descripción |
|--------|------|------|-------------|
| `GET` | `/v1/health` | No | Liveness probe. Retorna `{"status":"ok"}` |
| `GET` | `/v1/liveness` | No | Liveness probe (kubelet-friendly, no pingea deps). |
| `GET` | `/v1/readiness` | No | Readiness probe; pingea la DB cuando hay Postgres backend. |
| `POST` | `/v1/ingest-text` | Bearer (si configurado) | Ingesta de texto. Rate limit per-IP. Idempotente por `sha256(workspace, source, content)`. |
| `GET` | `/v1/search` | Bearer (si configurado) | Búsqueda FTS. Query params: `q`, `workspace_id`, `limit` |
| `GET` | `/v1/objects/{id}` | Bearer (si configurado) | Recupera un knowledge object por ID. Query param: `workspace_id` |
| `POST` | `/v1/check-collision` | Bearer (si configurado) | Detecta colisiones semánticas con knowledge existente. Body: `workspace_id`, `content`. Solo con Postgres + Gemini key. |
| `GET` | `/v1/backlog` | Bearer (si configurado) | Lista paginada (cursor) del human backlog. Query params: `workspace_id`, `page_size` (1..100, default 25), `cursor`. |
| `GET` | `/v1/sdd-document` | Bearer (si configurado) | Devuelve el SDD consolidado del workspace como Markdown. Query param: `workspace_id`. 404 si no existe. |

Ejemplo:
```sh
curl -G http://localhost:8050/v1/search \
  --data-urlencode "q=postgresql" \
  --data-urlencode "workspace_id=mi-ws" \
  --data-urlencode "limit=5" \
  -H "Authorization: Bearer $PROJECT_BRAIN_AUTH_TOKEN"
```

Retorna:
```json
{
  "query": "postgresql",
  "count": 3,
  "results": [
    {"object": {...}, "score": 0.95, "match_type": "fts"},
    ...
  ]
}
```

Ejemplo:
```sh
curl http://localhost:8050/v1/objects/11111111-1111-1111-1111-111111111111?workspace_id=mi-ws \
  -H "Authorization: Bearer $PROJECT_BRAIN_AUTH_TOKEN"
```

Retorna `{"object": {...}}` con el `KnowledgeObject` completo, o 404 si no existe.

Los endpoints `/v1/search` y `/v1/objects/{id}` solo se registran cuando hay Postgres backend (FTS necesita el `search_vector` column; in-memory mode no tiene corpus).

**Búsqueda full-text:** el FTS column existe y se popula automáticamente en cada ingest, y hay un endpoint HTTP `GET /v1/search` (ver arriba). Para inspeccionar la columna directamente:

```sh
docker exec hermes-agents-postgres-1 psql -U postgres -d project_brain -c \
  "SELECT id, title, ts_rank(search_vector, to_tsquery('simple', 'conocimiento')) AS rank
   FROM knowledge_objects
   WHERE search_vector @@ to_tsquery('simple', 'conocimiento')
   ORDER BY rank DESC"
```

### Bot de Telegram (opcional)

Si definís `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN`, el server arranca el bot además del HTTP API. Comandos disponibles:

- `/start` — mensaje de bienvenida
- `/help` — instrucciones
- Cualquier otro mensaje de texto → se ingesta como knowledge

### Servidor MCP (para agentes externos)

El binario `cmd/mcp` expone la knowledge API como **4 tools MCP** sobre **stdio JSON-RPC**. Agentes MCP-capable (Hermes, Claude, etc.) lo lanzan como subprocess y hablan JSON-RPC por stdin/stdout. **stdout es el canal de datos; stderr es para logs** — el agent debe separar ambos streams.

**Tools disponibles:**

| Tool | Argumentos | Descripción |
|------|------------|-------------|
| `search_knowledge` | `query` (req), `workspace_id?`, `limit?` | Búsqueda semántica (FTS + vector si hay Gemini key). |
| `check_collision` | `content` (req), `workspace_id?` | Detecta colisiones **sin almacenar**. Devuelve verdict (`duplicate` / `strong_overlap` / `related`). |
| `save_knowledge` | `content` (req), `workspace_id?`, `type?`, `title?` | Persiste un knowledge object. **Idempotente**: retries con mismo content en mismo workspace devuelven `duplicate: true` sin escribir filas nuevas. |
| `get_sdd_document` | `workspace_id?` | Devuelve el SDD document del workspace como Markdown. Si no existe, devuelve string humano-legible en vez de error. |

**Configuración del agente** (env vars al lanzar `cmd/mcp`):

| Variable | Default | Descripción |
|---|---|---|
| `PROJECT_BRAIN_API_URL` | `http://localhost:8050` | URL base de la API HTTP |
| `PROJECT_BRAIN_AUTH_TOKEN` | empty | Bearer token, si la API tiene auth habilitada |
| `PROJECT_BRAIN_MCP_WORKSPACE` | `default` | `workspace_id` por defecto cuando el agente omite el argumento |

**Rate limit y errores** (aplican a HTTP y MCP):
- 429 Too Many Requests con `Retry-After: 1` (default 5 RPS, burst 10 por IP, configurable)
- Error envelope: `{"error": "...", "message": "...", "code": "..."}` con códigos `VALIDATION_ERROR`, `NOT_FOUND`, `INVALID_CURSOR`, `PAYLOAD_TOO_LARGE`, `INTERNAL_ERROR`
- `X-Request-ID` se propaga (genera uno si el cliente no lo manda)
- El MCP client timeout es 30s por request

**Idempotencia de `save_knowledge`**: la dedup key es `sha256(workspace, "mcp", contentChecksum)` donde `contentChecksum = sha256(content)`. Re-invocar con el mismo `content` en el mismo `workspace_id` devuelve el objeto existente con `duplicate: true` y emite un único audit event "duplicate detected" — **no crea source/object/link nuevos**. El MCP layer no expone `source.idempotency_key`; si necesitás control fino, pegale a la API HTTP directo (`POST /v1/ingest-text`) con `source.idempotency_key` que toma precedencia sobre el hash.

---

## Tests

```sh
# Unit tests
go test ./...

# Integration tests contra Postgres real
PROJECT_BRAIN_TEST_DATABASE_DSN="postgres://postgres:postgres@127.0.0.1:5433/project_brain?sslmode=disable" \
  go test ./internal/postgres -v
```

---

## Configuración

| Variable | Default | Descripción |
|---|---:|---|
| `PROJECT_BRAIN_ENV` | `development` | Label del entorno de runtime. Si es `production` y no hay DSN, el server se niega a arrancar. |
| `PROJECT_BRAIN_API_PORT` | `8050` | Puerto TCP del server |
| `PROJECT_BRAIN_DATABASE_DSN` | empty | DSN de Postgres. Si está vacío, usa in-memory fake |
| `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` | empty | Token del bot. Si está vacío, el bot no arranca |
| `PROJECT_BRAIN_AUTH_TOKEN` | empty | Bearer token para `/v1/ingest-text`. Si está vacío, auth desactivada |
| `PROJECT_BRAIN_RATE_LIMIT_RPS` | `5` | Requests por segundo por IP (max 1000) |
| `PROJECT_BRAIN_RATE_LIMIT_BURST` | `10` | Burst máximo por IP (max 10000) |
| `PROJECT_BRAIN_TRUST_PROXY` | `false` | Si `true`, honra `X-Forwarded-For`. Default: solo usa `RemoteAddr` para evitar spoofing |
| `PROJECT_BRAIN_INGEST_MAX_BYTES` | `1 MiB` | Tamaño máximo del body en `/v1/ingest-text` |
| `PROJECT_BRAIN_LOG_LEVEL` | `info` (dev: `debug`) | Nivel de log: debug, info, warn, error |
| `PROJECT_BRAIN_SHUTDOWN_SECS` | `5` | Grace period para shutdown |
| `PROJECT_BRAIN_TEST_DATABASE_DSN` | empty | Habilita integration tests de Postgres |
| `PROJECT_BRAIN_MCP_WORKSPACE` | `default` | (Solo `cmd/mcp`) `workspace_id` por defecto cuando el agente omite el argumento |
| `PROJECT_BRAIN_SECURITY_HEADERS` | `true` | Setea los 6 headers OWASP 2025 baseline en cada response. `false` los desactiva por completo. |
| `PROJECT_BRAIN_TLS` | `false` | Emite `Strict-Transport-Security` cuando el API se sirve sobre HTTPS (típicamente detrás de un reverse proxy con TLS). |

---

## Estructura del proyecto

```
project-brain/
├── cmd/api/                          # Composition root (HTTP + Telegram)
├── cmd/mcp/                          # MCP stdio server entrypoint
├── internal/
│   ├── config/                       # Config desde env vars
│   ├── domain/                       # Entidades puras (Source, KnowledgeObject, Relation, AuditEvent)
│   ├── app/                          # Use cases (IngestTextService) + ports
│   ├── httpapi/                      # Adaptador HTTP (POST /v1/ingest-text, GET /v1/health)
│   ├── mcp/                          # Tools MCP para agentes
│   ├── telegram/                     # Adaptador Telegram (go-telegram/bot)
│   └── postgres/                     # Repositorio PostgreSQL (pgx/v5)
├── migrations/                       # SQL migrations
├── openspec/
│   ├── specs/                        # Source of truth de capabilities
│   └── changes/                      # Cambios activos y archive
├── Dockerfile
├── docker-compose.yml
├── ROADMAP.md                        # Estado del proyecto y planes futuros
├── PROJECT_BRAIN.md                  # Visión completa del producto
└── README.md                         # Este archivo
```

---

## Capacidades implementadas

| Capacidad | Spec | Descripción |
|-----------|------|-------------|
| `knowledge-core-ingestion` | [spec](openspec/specs/knowledge-core-ingestion/spec.md) | Ingesta de texto con auditoría, idempotencia, FTS |
| `telegram-bot-adapter` | [spec](openspec/specs/telegram-bot-adapter/spec.md) | Bot de Telegram que ingesta mensajes |
| `http-ingest-api` | (merged into knowledge-core-ingestion) | Adaptador HTTP |
| `knowledge-relations` | [spec](openspec/specs/knowledge-relations/spec.md) | Aristas tipadas entre knowledge objects |
| `fts-retrieval` | (en código, spec pendiente) | Búsqueda por keyword vía FTS |

Ver `ROADMAP.md` para el plan completo (Fase 2: Hybrid RAG, Fase 3: Human-in-the-Loop, Fase 4: Multi-agent).

---

## Stack

- **Go 1.25** — backend, workers, todo el código
- **PostgreSQL 16** (con pgvector) — base principal, FTS, transacciones, JSONB
- **pgx/v5** — driver Postgres (zero-dependency friendly)
- **go-telegram/bot** — librería de Telegram (zero deps, polling + webhook)
- **MCP stdio server** — expone search/collision/save como tools JSON-RPC para agentes
- **Docker** — Postgres + API en un `docker compose up`

## License

TBD
