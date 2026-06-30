# Design: embedded-migration-runner

## Technical Approach

`internal/migrations/` wraps `github.com/pressly/goose/v3` as a library. `main.go` opens one `pgxpool` from `PROJECT_BRAIN_DATABASE_DSN`, calls `migrations.Run` (probe + `goose.Up`), then hands the same pool to `runtime.BuildServicesWithPool`. Migrations ship via `//go:embed all:sql` (SQL lives inside the package so embed can reach it; `go:embed` cannot reach parent dirs). Implements `db-migrations` spec.

## Architecture Decisions

| # | Decision | Choice | Rejected | Rationale |
|---|----------|--------|----------|-----------|
| 1 | One pool, run before `BuildServices` | main: `pgxpool.New` → `migrations.Run` → `BuildServicesWithPool`; err → `os.Exit(1)` | (a) sequence after change-18 PR6 — moot; (b) two pools — wasteful; (c) goose CLI sidecar breaks single-binary deploy | Single pool eliminates A/B race. ~30 LOC additive in `services.go`; `BuildServices` stays as test wrapper. |
| 2 | Migration library | `github.com/pressly/goose/v3` embedded | `golang-migrate` CLI (process); lib (goose more Go-native with `embed.FS`); `atlas` (heavier); hand-rolled; `psql` (the bug) | Goose's API fits the embed-then-probe flow. |
| 3 | Initdb mount | Drop the bind mount; runner is the single source of truth | Keep mount (two paths = bug class) | Mount fires only on empty volume → desyncs. Drop line 17 + comment in compose. Embed alone is sufficient; the binary carries the SQL via `//go:embed`. No runtime mount or COPY required. |
| 4 | Baselining AND-condition | Probe = `to_regclass('knowledge_objects') IS NOT NULL` AND `schema_migrations` empty | Single-condition probe (any) | AND blocks the "wiped `schema_migrations` only" edge case. See tree below. |

**Decision tree (Decision 4)**

| Sentinel | Version table | Action |
|---|---|---|
| Present | Empty | **Baseline**: INSERT every on-disk version; no SQL; log `migrations baselined count=N`. |
| Absent | Empty | **Fresh**: run full chain ascending. |
| (any) | Non-empty | **Delta**: versions strictly greater than `MAX(version_id)`. |

## Startup Sequence

```
main.go
  1. config.Load
  2. enforceProductionAuth                          fail-closed (change-16)
  3. EnforceInMemoryProductionGuard                 fail-closed (change-19)
  4. pgxpool.New(ctx, cfg.DatabaseDSN)              ONE pool, used for migrations + services
  5. migrations.Run(ctx, pool, embedFS)
       ├─ err → slog.Error(version, error); os.Exit(1)  ── fail-fast, NO BuildServices
       └─ ok → continue
  6. runtime.BuildServicesWithPool(ctx, cfg, pool, logger)  UoW wraps SAME pool
  7. runtime.BuildServer(svcs, cfg, logger)
  8. server.HTTP.ListenAndServe (goroutine)
  9. signal.NotifyContext → runtime.RunShutdown
```

Invariant: 5 < 6 < 7 < 8.

## File Changes

(4 new, 7 modified, 1 moved)

| File | Action | Description |
|------|--------|-------------|
| `migrations/*.sql` (repo root) | Move | Relocate SQL files into the package as `internal/migrations/sql/*.sql` so `//go:embed` resolves. Filenames unchanged (0001..0015). The repo-root `migrations/` directory is removed in this move. |
| `internal/migrations/runner.go` | Create | `Run(ctx, pool, fsys) error`; `goose.Up` flow. |
| `internal/migrations/baseline.go` | Create | `probe(ctx, pool) (sentinel, empty, err)`; `baseline(...)`. |
| `internal/migrations/embed.go` | Create | `//go:embed all:sql`; exports `FS` rooted at `sql/`. |
| `internal/migrations/runner_test.go` | Create | Probe decision-tree unit tests (3 cases). |
| `internal/runtime/services.go` | Modify | Extract `BuildServicesWithPool`; `BuildServices` becomes thin wrapper. |
| `cmd/api/main.go` | Modify | Open pool → `migrations.Run` → `BuildServicesWithPool`; err → `os.Exit(1)`. |
| `Dockerfile` | No change | No change — embed bakes SQL into the binary at `go build` time; no runtime mount or COPY required. |
| `docker-compose.yml` | Modify | Drop line 17 mount + comment lines 12–16. |
| `go.mod` / `go.sum` | Modify | Add `github.com/pressly/goose/v3`. |
| `internal/postgres/ingestion_integration_test.go` | Modify | Keep raw-SQL `findMigrations` (rationale in Testing). |
| `docs/updating-on-vps.md` | Modify | Drop step 4 + TL;DR `psql` line. |
| `docs/deploy-vps.md` | Modify | Step 3: "API applies migrations on first start". |

## Interfaces / Contracts

```go
// internal/migrations/runner.go
package migrations

import (
    "context"
    "io/fs"
    "github.com/jackc/pgx/v5/pgxpool"
)

func Run(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error
```

Call site: `migrations.Run` (not `goose.Run`); embed in this package. Spec stays agnostic — design owns the "how".

## Testing Strategy

| Layer | What | Approach |
|---|---|---|
| Unit | Probe decision tree (3 cases); `baseline()` rows; `Run` idempotent | Real Postgres via test DSN; matches existing `internal/postgres/*_test.go` style. |
| Integration | `ingestion_integration_test.go` keeps raw-SQL `findMigrations` — needs a known schema snapshot, not goose's state machine (coupling would entangle test isolation) | No change. |
| E2E | N/A (`e2e.available: false`) | Manual: broken SQL → API restart-loops, `/v1/health` never 200 (fail-fast). |

## Migration / Rollout

3-PR chain. **change-18 coordination**: option (b) — `BuildServicesWithPool` (~30 LOC, additive) fits PR1. If PR6 is open, PR1 rebases.

| # | PR | Budget | Anchor |
|---|----|--------|--------|
| 1 | `migrations-runner-package` | ≤ 350 | goose dep; `internal/migrations/` (runner + probe + embed + tests); `BuildServicesWithPool`. |
| 2 | `main-wiring-and-compose-cleanup` | ≤ 200 | `main.go` opens pool → `migrations.Run` → `BuildServicesWithPool`; drop initdb mount + comment. |
| 3 | `docs-and-test-helper` | ≤ 200 | `updating-on-vps.md` step 4 + TL;DR stripped; `deploy-vps.md` step 3. |

## Open Questions

None.
