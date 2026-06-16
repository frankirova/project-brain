# Updating project-brain on a VPS

If you deployed with `docs/deploy-vps.md` and now `main` has new commits, this
is the full procedure to pull the new code without losing data, breaking
the agent, or skipping a migration.

The procedure is **the same every time** — the only thing that changes
between updates is whether new migrations landed. Read step 4 (the
migration gotcha) once and you have the full picture.

---

## 0. Pre-flight (always)

Connect to the VPS and confirm where you are:

```bash
cd ~/project-brain            # or wherever you cloned it
git status                    # should be clean
git branch --show-current     # should print: main
git log --oneline -1          # note the current commit hash, e.g. aa61822
```

If `git status` shows dirty files, stop and resolve that first (a leftover
edit, a half-finished merge, an uncommitted `.env` change). Pulling on a
dirty tree produces merge conflicts you do not want.

If `git branch --show-current` prints anything other than `main`, switch:

```bash
git fetch origin
git checkout main
```

The `feat/semantic-search-gemini` branch from older deploy docs is dead
and archived. Stay on `main`.

---

## 1. Pull the new code

```bash
git pull origin main
```

This updates the working tree to `origin/main`. The output will print
something like `Updating aa61822..bd52da7` (or `aa61822..<newer>` for
your next update). Note the new commit hash — you will need it for
step 5 verification.

`git pull` only updates source files. It does **not** rebuild anything
or restart anything. That comes next.

---

## 2. Rebuild and restart the API

The API is the Docker container named `api` in `docker-compose.yml`.
`docker compose up -d --build` rebuilds the image from the new source
and restarts only the changed containers (Postgres stays up, its data
volume is untouched).

```bash
docker compose up -d --build
```

Wait for the API to be healthy:

```bash
docker compose ps                       # api should be 'healthy' or 'running'
curl -fsS http://localhost:8050/v1/health
# {"status":"ok"}
curl -fsS http://localhost:8050/v1/liveness
# {"status":"ok"}
```

`/v1/liveness` and `/v1/readiness` are the kubelet / load-balancer
probes. They do not require auth. If liveness is OK, the API process
itself is up.

If you have `PROJECT_BRAIN_GEMINI_API_KEY` set, also confirm hybrid
search loaded:

```bash
docker compose logs api --tail=50 | grep -i "hybrid search"
# hybrid search + collision detection enabled provider=gemini ...
```

If the log says `search enabled (fts only)`, the Gemini key did not
load — recheck `.env` and re-run step 2.

---

## 3. Rebuild the MCP binary

The MCP server is a **separate binary** that the agent launches as a
subprocess. It is **not** inside the Docker container — you build it
on the host so the agent can `exec` it. The agent's config (step 5 of
`docs/deploy-vps.md`) points to its absolute path, so the binary on
disk MUST match the new source.

If you already have Go 1.25 on the VPS:

```bash
go build -o bin/project-brain-mcp ./cmd/mcp
```

If you do NOT have Go on the VPS (production best practice — smaller
attack surface), use the Docker toolchain the same way as the
first-time install:

```bash
docker run --rm -v "$PWD":/app -w /app golang:1.25-alpine \
  go build -o bin/project-brain-mcp ./cmd/mcp
```

The result is a Linux binary at `bin/project-brain-mcp`. Same path the
agent's MCP config already references — no config change needed if the
path did not move.

---

## 4. The migration gotcha (read carefully)

New migrations in `migrations/*.sql` apply automatically **only on an
empty data volume** (first install, or after `docker compose down -v`
which destroys data). On an existing database, you must apply them by
hand.

**How to know if a new update includes a migration:**

```bash
git log --oneline <previous-commit>..HEAD -- migrations/
```

If that prints commits, the update includes SQL migration files.
Replace `<previous-commit>` with the hash you noted in step 1, and
`HEAD` is your new state.

Examples:

- Empty output → no migrations in this update. Skip the rest of step 4.
- `7a3b2c1 feat(knowledge-objects): add per-object language column`
  → migration 0016 or similar landed.

**If there is a new migration, apply it manually:**

```bash
# List the new migration files since your last update
git diff --name-only <previous-commit>..HEAD -- migrations/

# Apply them in order, one at a time, INSIDE the postgres container.
# The container is named 'postgres' in docker-compose.yml.
docker compose exec -T postgres psql -U postgres -d project_brain < migrations/00NN_whatever.sql
```

The `-T` flag disables pseudo-TTY allocation, which is required to
pipe stdin into `psql` from a non-interactive shell.

After applying, sanity-check that the new schema is live:

```bash
docker compose exec -T postgres psql -U postgres -d project_brain -c "\d knowledge_objects"
```

(Replace `knowledge_objects` with the table the new migration touched.)

**If the update says "no migrations in this round"** (e.g. PR #27
"RFC 9457 + OWASP headers" was pure Go + docs, no SQL) you can skip
this entire step. The next `git pull` is harmless on the existing DB.

---

## 5. Restart the agent

The agent holds a long-lived stdio pipe to the MCP subprocess. Pulling
and rebuilding the binary does not invalidate that pipe automatically —
the agent is still talking to the old binary in memory. You must
restart the agent to make it pick up the new binary.

How you restart depends on your agent:

- **Hermes on the same VPS** as a systemd service: `systemctl restart hermes`
- **Claude Desktop**: close the app and reopen it
- **Any other MCP client**: kill the parent process and relaunch

After restart, the agent should list 4 tools (not 3) if you updated past
PR #27:
- `search_knowledge`
- `check_collision`
- `save_knowledge`
- `get_sdd_document`

If the agent only lists 3, it is still running the old binary — check
the absolute path in its MCP config matches `bin/project-brain-mcp` on
disk.

---

## 6. End-to-end smoke test (recommended after every update)

The cheapest way to confirm the new code is live and reachable through
every layer:

```bash
# 1) API is up
curl -fsS http://localhost:8050/v1/liveness

# 2) Security headers landed (PR #27+)
curl -fsSI http://localhost:8050/v1/liveness | grep -iE "x-frame-options|x-content-type-options|cache-control"
# Expect 3 lines, all with non-empty values.

# 3) Problem+json opt-in works (PR #27+)
curl -fsS -i -H "Accept: application/problem+json" \
  -X POST http://localhost:8050/v1/ingest-text 2>&1 | head -20
# Expect: HTTP/1.1 4xx, Content-Type: application/problem+json,
# body has type/title/status/detail/instance fields.

# 4) MCP round-trip through the agent: ask the agent to call
#    save_knowledge twice with the same content. Second call MUST
#    return duplicate=true (idempotency contract documented in
#    PR #26 / README).
```

If any of those fail, do not roll back the binary yet — first check
the API logs:

```bash
docker compose logs api --tail=100
```

The log line for the failing call will name the cause (missing env
var, DB connection refused, handler panic, etc.).

---

## 7. Rollback (if the new version is broken)

The procedure is the reverse of update. You need:
- The previous commit hash (you noted it in step 1)
- The previous MCP binary (you do **not** have a backup of the old
  binary unless you saved `bin/project-brain-mcp` before rebuilding)

If you did save the old binary:

```bash
# Stop the agent first so it does not keep using the broken binary
systemctl stop hermes                       # or however you stop it

# Revert the code
git checkout <previous-commit>

# Rebuild API with the old code
docker compose up -d --build

# Restore the old MCP binary
cp /path/to/saved/project-brain-mcp bin/project-brain-mcp

# Restart the agent
systemctl start hermes
```

If you did **not** save the old binary (the common case), the rollback
is harder:

```bash
git checkout <previous-commit>
docker compose up -d --build
go build -o bin/project-brain-mcp ./cmd/mcp    # rebuilds the OLD code
# (or: docker run --rm -v "$PWD":/app -w /app golang:1.25-alpine
#   go build -o bin/project-brain-mcp ./cmd/mcp)
```

You lose the new binary but you recover the old behavior. Then revert
your working tree back to `main` once you understand what broke:

```bash
git checkout main
git pull origin main
```

If a migration in the bad update touched the schema, rolling back the
code is not enough — you also need to manually reverse the migration
SQL. This is why we test in a branch or staging environment first when
the change is large.

---

## 8. TL;DR for the next update

```bash
cd ~/project-brain
git pull origin main
docker compose up -d --build
docker run --rm -v "$PWD":/app -w /app golang:1.25-alpine \
  go build -o bin/project-brain-mcp ./cmd/mcp
# If new migrations in migrations/ (check with `git log --oneline
#   <prev>..HEAD -- migrations/`), apply them by hand:
#   docker compose exec -T postgres psql -U postgres -d project_brain < migrations/00NN_xxx.sql
systemctl restart hermes    # or however you restart the agent
curl -fsS http://localhost:8050/v1/liveness
```

That is the whole procedure. It takes about 2 minutes when there are no
migrations, and 5 minutes when there are.
