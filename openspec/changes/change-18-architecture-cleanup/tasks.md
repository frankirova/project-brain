# Tasks: change-18 — architecture cleanup

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: feature-branch-chain
400-line budget risk: Low (per PR; cumulative ~1.6k intentionally above 400)

### Suggested Work Units

| # | Goal | Branch | Base |
|---|---|---|---|
| 1 | golangci-lint v2 + CI | `feat/change-18-pr1-golangci-lint-v2-baseline` | tracker |
| 2 | DTOs out of `app/ports.go` | `feat/change-18-pr2-ports-dtos-extract` | PR1 |
| 3 | Split `repositories.go` by UoW | `feat/change-18-pr3-postgres-repos-split` | PR2 |
| 4 | Split `object_debate.go` per UC | `feat/change-18-pr4-object-debate-split` | PR3 |
| 5 | `New(Config)` + render split | `feat/change-18-pr5-telegram-handler-refactor` | PR4 |
| 6 | `main.go` slim; runtime | `feat/change-18-pr6-main-runtime-extract` | PR5 |

### PR1 (≤ 400)

- [x] 1.1 Create `.golangci.yml` v2 (`version: "2"`); linters per #1741
- [x] 1.2 Add `linters.exclusions.paths` baseline for 6 paths
- [x] 1.3 Add `.github/workflows/lint.yml`; `golangci/golangci-lint-action@v6` v2.12.2
- [x] 1.4 Add `make lint` target
- [x] 1.5 `golangci-lint run` config valid + trips on 57 known-debt violations
      _(see deviation note in `openspec/changes/change-18-architecture-cleanup/apply-progress-pr1.md`)_
- [x] 1.6 CI workflow configured; will report debt on first run _(intentional — see deviation note)_
**Verify** — `repo-quality` §"v2 active in CI" + §"baseline ignores" + §"local parity via Make"

### PR2 (≤ 200)

- [x] 1.1 Create `internal/telegram/dto.go` (`BacklogViewItem`, `BacklogAction`)
- [x] 1.2 Create `internal/embeddings/dto.go` with embedding DTOs
- [x] 1.3 Strip DTOs from `internal/app/ports.go`; interfaces only (≤ 80 LOC)
      _(see deviation note in `openspec/changes/change-18-architecture-cleanup/apply-progress-pr2.md` — port-contract structs stay in app per decision (c) of the orchestrator brief; constants + new viewmodel types moved out)_
- [x] 1.4 Update `internal/app/` imports to new DTO pkgs
- [x] 1.5 `go build ./...` + `go vet ./...` clean
- [x] 1.6 `go test -short ./...` green; deps exclude telegram/embeddings
**Verify** — `telegram-bot-adapter` §"DTOs live in adapter packages"

### PR3 (≤ 400)

- [ ] 1.1 Read `repositories.go` + `db.go`; map UoW bundles
- [ ] 1.2 Create `object_relations_repo.go`
- [ ] 1.3 Create `sdd_documents_repo.go`
- [ ] 1.4 Create `raw_inputs_repo.go`
- [ ] 1.5 Create `knowledge_objects_repo.go`
- [ ] 1.6 Create per-table files: audit_events, review_actions, pending_validations
- [ ] 1.7 Delete `repositories.go`
- [ ] 1.8 `WithinIngestionTx` in `db.go` byte-identical
- [ ] 1.9 `go build ./...` clean; `go test ./internal/postgres/...` green
- [ ] 1.10 `go test -short ./...` green; each `*_repo.go` ≤ 200 LOC
**Verify** — `knowledge-core-ingestion` §"repo files split by UoW" + §"no behavior change"

### PR4 (≤ 400)

- [ ] 1.1 Read `object_debate.go`; identify 4 use cases
- [ ] 1.2 Create `object_debate_backlog.go`
- [ ] 1.3 Create `object_debate_validate.go`
- [ ] 1.4 Create `object_debate_discard.go`
- [ ] 1.5 Create `object_debate_decide.go`
- [ ] 1.6 Create `object_debate_helpers.go` (target guard, metadata)
- [ ] 1.7 Delete `object_debate.go`
- [ ] 1.8 `go build ./...` + `go vet ./...` clean
- [ ] 1.9 `go test -short ./...` green; change-16/17 integration unchanged
**Verify** — no spec; postgres integration suite green is the merge gate

### PR5 (≤ 400)

- [ ] 1.1 Read `handler.go`; list 7 `NewHandler*` overloads + deps
- [ ] 1.2 Create `config.go` with `Config` struct
- [ ] 1.3 Add `(*Config).applyDefaults()` + `Validate()` (cross-field)
- [ ] 1.4 Add `New(Config) (*Handler, error)` → defaults+Validate+seam
- [ ] 1.5 Make `newHandlerWithStore` the private same-package seam
- [ ] 1.6 Delete 6 legacy `NewHandler*`; `rg "func NewHandler" internal/telegram` = 0
- [ ] 1.7 Create `handler_render.go` (`BuildBacklogView`, `BuildResolveView`)
- [ ] 1.8 Create `handler_render_telegram.go`; move `bot.NewInlineKeyboardButton*`
- [ ] 1.9 `handler.go` calls viewmodel+render; drop `models.*` imports
- [ ] 1.10 `handler.go` ≤ 350 lines; `InlineKeyboardMarkup` only in render file
- [ ] 1.11 `go test -short ./...` green; tests use seam; `go vet ./...` clean
**Verify** — `telegram-bot-adapter` §"single constructor with Config" + §"render split"

### PR6 (≤ 400)

- [ ] 1.1 Read `internal/runtime/runtime.go`; identify `Build*` peers
- [ ] 1.2 Add `telegram_runtime.go` with `BuildTelegramBot`
- [ ] 1.3 Add `shutdown.go` with `RunShutdown`
- [ ] 1.4 Wire `BuildTelegramBot`; return opaque `runtime.TelegramBot`
- [ ] 1.5 `main.go` calls `BuildTelegramBot` after `BuildServer`
- [ ] 1.6 Replace inline shutdown with `runtime.RunShutdown`; drop `signal.Notify`
- [ ] 1.7 Keep `enforceProductionAuth` first after `config.Load()`
- [ ] 1.8 `main.go` ≤ 60 lines; `rg "internal/telegram" cmd/api/main.go` = 0
- [ ] 1.9 `go build ./...` + `go vet ./...` clean
- [ ] 1.10 `go test -short ./...` green; `main_test.go` invariants pass unchanged
- [ ] 1.11 `golangci-lint run` green
**Verify** — `http-api-runtime` §"composition root" + §"bot composition" + §"shutdown" + §"startup invariants"
