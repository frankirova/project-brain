# Deploy project-brain on a VPS

Step-by-step to run the backend (API + Postgres + pgvector) on a Linux
VPS and wire its MCP server into a local Hermes (or any MCP) agent.

The stack runs in Docker. The only thing you build outside Docker is the
small MCP binary the agent launches as a subprocess.

---

## 0. Prerequisites

On the VPS (Ubuntu/Debian assumed):

- **Docker + Compose plugin**
  ```bash
  docker --version
  docker compose version
  ```
  If missing: `curl -fsSL https://get.docker.com | sh`
- A **Gemini API key** (https://aistudio.google.com/apikey). Required for
  semantic search and collision detection.
- Your **Hermes agent** running on the same VPS (for the stdio MCP transport).

---

## 1. Get the code

First time:
```bash
git clone https://github.com/frankirova/project-brain.git
cd project-brain
git checkout main
```

Updating an existing checkout: see `docs/updating-on-vps.md` for the full procedure.

---

## 2. Create the secrets file

The API reads secrets from a `.env` file (git-ignored — never committed).
Create it in the repo root:

```bash
cat > .env <<'EOF'
PROJECT_BRAIN_GEMINI_API_KEY=YOUR_GEMINI_KEY_HERE
PROJECT_BRAIN_AUTH_TOKEN=pick-a-long-random-token
EOF
```

- `PROJECT_BRAIN_GEMINI_API_KEY` — without it, ingest still works but
  search/collision endpoints are disabled (and the MCP tools fail).
- `PROJECT_BRAIN_AUTH_TOKEN` — protects every endpoint except `/v1/health`.
  Strongly recommended on a public VPS. Use the **same** value in the
  Hermes MCP config (step 5).

Generate a token quickly:
```bash
openssl rand -hex 32
```

---

## 3. Start the stack

```bash
docker compose up -d --build
```

This builds the API image, starts Postgres (pgvector), auto-applies all
migrations on the **first** run (empty data volume), and launches the API
on port `8050`.

Wait for health:
```bash
docker compose ps
curl -s http://localhost:8050/v1/health
# {"status":"ok"}
```

Check the logs say hybrid search is on:
```bash
docker compose logs api | grep -i "hybrid search"
# ... "hybrid search + collision detection enabled" provider=gemini ...
```
If you instead see `search enabled (fts only)`, the Gemini key did not
load — recheck `.env` and `docker compose up -d` again.

---

## 4. Smoke-test the API

Replace `$TOKEN` with your `PROJECT_BRAIN_AUTH_TOKEN`.

```bash
TOKEN=pick-a-long-random-token
WS=default

# 1) Save a decision
curl -s -X POST http://localhost:8050/v1/ingest-text \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"workspace_id\":\"$WS\",\"type\":\"decision\",\"content\":\"El equipo usa Go para el backend\"}"

# 2) Search by meaning
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8050/v1/search?workspace_id=$WS&q=lenguaje%20del%20servidor"

# 3) Collision check (the killer)
curl -s -X POST http://localhost:8050/v1/check-collision \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"workspace_id\":\"$WS\",\"content\":\"Propongo usar Python en vez de Go\"}"
```

Step 3 should return the Go decision as a collision.

---

## 5. Build the MCP binary and wire Hermes

The MCP server is a tiny binary the agent launches over stdio. It forwards
tool calls to the API on `localhost:8050`, so it only needs the API URL
and token — never the database credentials.

**Build it (no Go install needed — use the Docker toolchain):**
```bash
docker run --rm -v "$PWD":/app -w /app golang:1.25-alpine \
  go build -o bin/project-brain-mcp ./cmd/mcp
```
This produces `bin/project-brain-mcp` (a Linux binary). If you already have
Go 1.25 on the VPS, `go build -o bin/project-brain-mcp ./cmd/mcp` works too.

**Register it in your Hermes MCP config** (standard MCP server shape —
adapt key names if Hermes differs):
```json
{
  "mcpServers": {
    "project-brain": {
      "command": "/absolute/path/to/project-brain/bin/project-brain-mcp",
      "env": {
        "PROJECT_BRAIN_API_URL": "http://localhost:8050",
        "PROJECT_BRAIN_AUTH_TOKEN": "pick-a-long-random-token",
        "PROJECT_BRAIN_MCP_WORKSPACE": "default"
      }
    }
  }
}
```

Restart Hermes. It should now expose four tools: `search_knowledge`,
`check_collision`, `save_knowledge`, `get_sdd_document`.

---

## 6. Hardening (public VPS)

- **Auth token**: set `PROJECT_BRAIN_AUTH_TOKEN` (step 2). Done = every
  endpoint but `/v1/health` requires the Bearer token.
- **TLS / HSTS**: if the API is behind a TLS-terminating reverse proxy
  (Nginx, Caddy, Cloudflare Tunnel, etc.), set `PROJECT_BRAIN_TLS=1` in
  `.env`. The API then emits `Strict-Transport-Security: max-age=63072000;
  includeSubDomains` on every response. Default off so dev / test
  instances running on plain HTTP do not advertise HTTPS upgrades.
- **Security headers**: on by default (`PROJECT_BRAIN_SECURITY_HEADERS=true`).
  6 OWASP 2025 baseline headers (X-Content-Type-Options, X-Frame-Options,
  Referrer-Policy, Permissions-Policy, Cross-Origin-Resource-Policy,
  Cache-Control: no-store) are added to every response, including
  `/v1/health`, `/v1/liveness`, and `/v1/readiness` (which your
  kubelet / load balancer uses — these endpoints are also bound to
  localhost via the next bullet).
- **Don't expose Postgres/API publicly.** Bind the API to localhost by
  editing `docker-compose.yml`:
  ```yaml
  api:
    ports:
      - "127.0.0.1:8050:8050"   # was "8050:8050"
  ```
  Same idea for the `postgres` port if you don't need it from outside.
  Then `docker compose up -d`.
- Or put a firewall (ufw) in front and only allow what you need.

---

## 7. Updating later

See `docs/updating-on-vps.md` for the full procedure (3 rebuilds, migration gotcha, verification, rollback).

---

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| `/v1/search` or `/v1/check-collision` → 404 | No Gemini key loaded → check `.env`, restart. |
| Any protected endpoint → 401 | Missing/wrong `Authorization: Bearer <token>`. |
| Logs say `running with in-memory uow` | `PROJECT_BRAIN_DATABASE_DSN` not set — compose sets it; you likely ran the API outside compose. |
| `relation "embeddings" does not exist` | Old data volume from before the migration. `docker compose down -v` to wipe (destroys data) or apply the migration manually. |
| MCP tools error "connection refused" | API not running, or wrong `PROJECT_BRAIN_API_URL` in the Hermes config. |
| Collision verdict seems off | Verdict bands are tuned for Gemini Spanish (~0.6 unrelated, ~0.78 collision, ~0.90 duplicate). |
