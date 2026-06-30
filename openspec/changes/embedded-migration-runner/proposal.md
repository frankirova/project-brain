# Proposal: embedded-migration-runner

> Embed `goose` as a Go library in API startup, fail-fast on migration error, own the schema lifecycle. 2–3 PR chain on `feature/embedded-migration-runner`, each ≤ 400 lines, each reverts cleanly. Kills the manual `psql` step in `docs/updating-on-vps.md`.

## Intent

Migrations in `migrations/0001..0015_*.sql` are applied via two disjoint paths: (1) fresh install uses the Postgres image's `/docker-entrypoint-initdb.d` mount, which fires **only on an empty data volume**; (2) existing-DB updates require `docker compose exec postgres psql … < 00NN_*.sql` by hand per `docs/updating-on-vps.md` step 4. The Go API runs no migrations today — no `goose`, no `schema_migrations`, no `golang-migrate` (`internal/postgres/pending_validations.go:30-31`, `telegram_review_actions.go:23`). This change embeds `github.com/pressly/goose/v3` in `cmd/api/main.go` before `http.Server.ListenAndServe()`, with a `schema_migrations` version table.

## Scope

**In**: `internal/migrations/` package (runner + baselining probe + `embed.FS` over `./migrations`); `goose` dep; call site in `cmd/api/main.go`; remove `./migrations:/docker-entrypoint-initdb.d:ro` mount + rewrite comment; `Dockerfile` `COPY ./migrations`; `docs/updating-on-vps.md` step 4 + TL;DR; `docs/deploy-vps.md` step 3; integration test helper.

**Out**: new SQL, schema changes, downgrade tooling, switching migration libraries.

## Capabilities

**New**: `db-migrations` — schema is versioned, tracked in a version table, applied automatically on API startup before serving traffic, idempotent, fail-fast. `goose` is one implementation; the spec is the contract. Becomes `openspec/specs/db-migrations/spec.md` (new full spec — verified: no existing spec covers migration lifecycle).

**Modified**: None. The three existing specs are channel/domain contracts; this is startup wiring.

## Approach

New package `internal/migrations/` (not `internal/postgres/`: migrations are schema lifecycle, not data access). `main.go` opens a `pgxpool` from `PROJECT_BRAIN_DATABASE_DSN`, runs migrations, then hands the pool to `BuildServices` — so migration failure exits the process **before** any service is constructed.

**Baselining (first-class)**: the deployed DB already has 0001..0015 applied via the old initdb hook. Runner probes for the sentinel `knowledge_objects` table (from `0001_knowledge_core_ingestion.sql`). If the sentinel exists AND `schema_migrations` is empty → INSERT max-on-disk version (0015) into `schema_migrations` and skip the SQL files. Sentinel absent + version table empty → run the full chain. Version table non-empty → run only the delta. This is the goose `SetBaseVersion` pattern, expressed as a probe.

**Initdb mount**: remove. With the runner as the single source of truth, the hook becomes a parallel path that can desync. Tradeoff: an undocumented local-dev loop that relied on the bind mount must rebuild the image instead — that loop was already broken on existing volumes.

| # | PR | Budget | Anchor |
|---|---|---|---|
| 1 | `migrations-runner-package` | ≤ 350 | `go.mod` adds `goose`; new `internal/migrations/` with runner + baselining probe + unit tests; `Dockerfile` `COPY ./migrations` |
| 2 | `main-wiring-and-compose-cleanup` | ≤ 200 | `main.go` opens pool → runner → hands pool to `BuildServices`; remove initdb mount + rewrite comment |
| 3 | `docs-and-test-helper` | ≤ 200 | strip `updating-on-vps.md` step 4 + TL;DR psql line; `deploy-vps.md` step 3 wording; decide `ingestion_integration_test.go` `findMigrations` (switch to goose library or keep raw-SQL with rationale) |

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `cmd/api/main.go` | Modified | Open pool → run migrations → pass pool to `BuildServices`; error → `os.Exit(1)` |
| `internal/migrations/` | New | Runner, baselining probe, `embed.FS` |
| `go.mod` / `go.sum` | Modified | Add `github.com/pressly/goose/v3` |
| `Dockerfile` | Modified | `COPY ./migrations /app/migrations` so `embed.FS` resolves |
| `docker-compose.yml` | Modified | Remove line 17 mount; rewrite comment 12-16 |
| `internal/postgres/ingestion_integration_test.go` | Modified (PR3) | `findMigrations` decision: goose library vs raw-SQL |
| `docs/updating-on-vps.md` | Modified (PR3) | Drop step 4 manual-psql block; drop TL;DR psql line |
| `docs/deploy-vps.md` | Modified (PR3) | Step 3: "applies migrations on first API start" |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Bad migration causes restart loop, API unreachable | Med | **Intended behavior** — schema errors surface immediately, not as a half-migrated DB. `docker compose logs api` names the file/version. Rollback = revert commit. |
| Baselining misfires on partial fresh install (sentinel absent, operator wiped `schema_migrations` only) | Low | Probe checks sentinel AND empty version table together. Mid-deploy operator intervention is not a supported path; documented in spec. |
| Goose upstream breaking change | Low | Pin minor version; `go.mod` lock; new change to upgrade. |
| `embed.FS` misses a new migration file | Low | `go build` fails if embed path is wrong; PR template reminder. |
| Removing initdb mount breaks a local-dev loop | Low | Loop was already broken on existing volumes (the gotcha); document in PR2. |

## Rollback

Revert the chain + rebuild + redeploy the old image. The DB is **unaffected**: goose only ADDS the `schema_migrations` table and ADDs version rows; it does not drop or alter existing data. If the initdb mount was removed, reverting restores it. **Edge case**: if the new image baselined an existing DB (inserted 0015) and you roll back to the old image, the old image's compose still has the initdb mount but for the EXISTING volume it never fires again — the leftover `schema_migrations` table is inert noise, not corruption (goose never DDLs tables other than its own version table).

## Dependencies

- `internal/runtime` (change-18 PR6) — owns pool construction that `main.go` will now hand-build before `BuildServices`.
- `PROJECT_BRAIN_DATABASE_DSN` (already set in `docker-compose.yml:29`).
- `migrations/` reachable at runtime — satisfied by `Dockerfile` `COPY` in PR1.
- Go 1.25 (already in use).

## Success Criteria

- [ ] `cmd/api/main.go` runs the migration runner before `http.Server.ListenAndServe()`; error → process exits non-zero with a structured log naming the failing file/version.
- [ ] Existing DB (0001..0015 already applied via the old initdb hook) starts cleanly under the new image: no "relation already exists", `schema_migrations` populated with 0015, all endpoints healthy.
- [ ] Fresh install (empty `pgdata`): schema fully created, `schema_migrations` populated with 0015, API healthy.
- [ ] A new SQL file in `migrations/` is applied on the next `docker compose up -d --build` with zero operator action.
- [ ] A deliberately broken SQL file (missing table) causes the API container to exit and restart-loop; `/v1/health` never returns 200 during the loop (proves fail-fast).
- [ ] `docs/updating-on-vps.md` step 4 is removed; the TL;DR no longer mentions `psql` or `migrations/` manual application.
- [ ] `go test ./...` green; `go vet ./...` clean; `gofmt -l` clean; `go build ./cmd/api ./cmd/mcp` succeeds.
- [ ] All 3 PRs merged in chain order, each ≤ 400 lines review budget.
