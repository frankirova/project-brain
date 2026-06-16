# Tasks: embedded-migration-runner

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | 250-350 per PR; ~750 total across chain |
| 400-line budget risk | Low |
| Chained PRs recommended | Yes |
| Suggested split | PR1 (foundation) → PR2 (wiring) → PR3 (docs) |
| Delivery strategy | auto-chain |
| Chain strategy | feature-branch-chain |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: feature-branch-chain
400-line budget risk: Low

### Suggested Work Units

| Unit | Goal | PR | Base branch boundary | Notes |
|------|------|----|----------------------|-------|
| 1 | `migrations-runner-package` | PR1 | `feature/embedded-migration-runner` (tracker) | goose dep; `internal/migrations/` (runner, probe, embed, tests); `BuildServicesWithPool`. ≤350. |
| 2 | `main-wiring-and-compose-cleanup` | PR2 | PR1's branch | Open pool → `migrations.Run` → `BuildServicesWithPool`; drop initdb mount. ≤200. |
| 3 | `docs-and-test-helper` | PR3 | PR2's branch | Strip manual `psql`; document test-helper rationale. ≤200. |

## Phase 1: Foundation (PR1)

- [x] 1.1 `git mv migrations/0001..0015_*.sql internal/migrations/sql/`; verify repo-root `migrations/` dir gone (`ls migrations` → not found).
- [x] 1.2 Add `github.com/pressly/goose/v3` to `go.mod`; run `go mod tidy`; verify `go build ./...` passes.
- [x] 1.3 Create `internal/migrations/embed.go` with `//go:embed all:sql`; export `FS fs.FS` rooted at `sql/`.
- [x] 1.4 Create `internal/migrations/baseline.go`: `probe(ctx, pool) (sentinelExists, versionTableEmpty bool, err error)` using `to_regclass('knowledge_objects') IS NOT NULL` + `SELECT COUNT(*)` on version table; `baseline(ctx, pool, versions []string) error` inserts rows in one tx.
- [x] 1.5 Create `internal/migrations/runner.go`: `Run(ctx, pool, fsys) error` — `probe` → branch baseline/fresh/delta → `goose.Up`; `slog.Error(version, err)` on failure.
- [x] 1.6 Create `internal/migrations/runner_test.go` (env-gated `PROJECT_BRAIN_TEST_DATABASE_DSN`). Cover 3 probe branches: sentinel+empty → baseline (no SQL executed, version rows inserted); sentinel absent+empty → fresh (all 15 applied); version table non-empty → delta (16th file added, only 16th applied). Covers spec §Baselining both scenarios, §Idempotent Startup, §Delta-Only Updates both scenarios.
- [x] 1.7 Modify `internal/runtime/services.go` (additive): extract `BuildServicesWithPool(ctx, cfg, pool, logger)` from `BuildServices` after `postgres.Open`; `BuildServices` becomes thin wrapper. `boot_test.go`, `boot_dsn_test.go`, `shutdown_test.go`, `server_test.go`, `typed_nil_test.go`, `server_headers_test.go` MUST stay green.

## Phase 2: Integration (PR2)

- [x] 2.1 Modify `cmd/api/main.go`: open one `pgxpool` from `cfg.DatabaseDSN`; call `migrations.Run(ctx, pool, migrations.FS)`; on error `slog.Error(version, err)` then `os.Exit(1)`; hand same pool to `runtime.BuildServicesWithPool`. Preserve ordering 5<6<7<8. Covers spec §Auto-Apply on Startup, §Fail-Fast on Migration Error.
- [x] 2.2 Modify `docker-compose.yml`: delete `./migrations:/docker-entrypoint-initdb.d:ro` mount (line 17); rewrite comment lines 12-16 to "migrations applied by the API on startup"; verify `docker compose config` parses.
- [x] 2.3 Verify `Dockerfile` needs NO change (SQL embedded at build time via `//go:embed`; no runtime COPY/mount required). Confirm by reading COPY instructions.

## Phase 3: Documentation & Polish (PR3)

- [ ] 3.1 Modify `docs/updating-on-vps.md`: delete step 4 manual `psql` block (lines ~123-171); delete `psql` line in TL;DR (lines ~289-302); reframe "Migrations" as "auto-applied on API startup, fail-fast on error".
- [ ] 3.2 Modify `docs/deploy-vps.md` step 3 wording: "API applies migrations on first start" (replaces initdb-hook narrative); reference spec §Auto-Apply on Startup.
- [ ] 3.3 Add one-line comment at top of `findMigrations` in `internal/postgres/ingestion_integration_test.go` explaining WHY raw-SQL is kept (per design §Testing Strategy — avoids entangling test isolation with goose state machine).
