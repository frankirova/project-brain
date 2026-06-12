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
| `POST` | `/v1/ingest-text` | Bearer (si configurado) | Ingesta de texto. Rate limit per-IP |
| `GET` | `/v1/search` | Bearer (si configurado) | Búsqueda FTS. Query params: `q`, `workspace_id`, `limit` |
| `GET` | `/v1/objects/{id}` | Bearer (si configurado) | Recupera un knowledge object por ID. Query param: `workspace_id` |

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

### Bot de Telegram (opcional)

Si definís `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN`, el server arranca el bot además del HTTP API. Comandos disponibles:

- `/start` — mensaje de bienvenida
- `/help` — instrucciones
- Cualquier otro mensaje de texto → se ingesta como knowledge

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

---

## Estructura del proyecto

```
project-brain/
├── cmd/api/                          # Composition root (HTTP + Telegram)
├── internal/
│   ├── config/                       # Config desde env vars
│   ├── domain/                       # Entidades puras (Source, KnowledgeObject, Relation, AuditEvent)
│   ├── app/                          # Use cases (IngestTextService) + ports
│   ├── httpapi/                      # Adaptador HTTP (POST /v1/ingest-text, GET /v1/health)
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
├── GO_CODE_WALKTHROUGH.md            # Paseo por el código (intro Go)
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
- **Docker** — Postgres + API en un `docker compose up`

## License

TBD
