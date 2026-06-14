# Proposal: change-18 — architecture cleanup

> Six-smell hexagonal refactor of project-brain (Go 1.25 + PG16, ~21k LOC). Six chained PRs on `feature/change-18-architecture-cleanup`, each ≤ 400 lines, each reverts cleanly.

## Intent

Six structural smells block scale: no v2 linter; DTOs leaking through `internal/app/ports.go` (384 LOC); 528-LOC postgres mega-file; 492-LOC multi-use-case service; 992-LOC `internal/telegram/handler.go` with seven `NewHandler*` overloads; 263-LOC `cmd/api/main.go` mixing composition with shutdown. They block Fase 4. Research in Engram #1736–#1743 backs every verdict.

## Scope

**In**: `.golangci.yml` (v2) + `.github/workflows/lint.yml` (golangci-lint v2.12.x, released 2025-03-24); DTOs from `internal/app/ports.go` → adapter packages; `repositories.go` split by UoW bundle preserving `WithinIngestionTx` in `db.go`; `object_debate.go` split per use case; `handler.go` → one `New(Config) (*Handler, error)` per #1736 + private `newHandlerWithStore`; `cmd/api/main.go` slimmed, composition + shutdown → `internal/runtime/`.

**Out**: new domain behavior; migrations; new public APIs; linter tightening beyond baseline.

## Capabilities

**New**: `http-api-runtime` (composition root); `repo-quality` (linter v2 baseline + 3-phase rollout).
**Modified**: `telegram-bot-adapter` (PR2, PR5; interfaces stable); `knowledge-core-ingestion` (PR3; 4-write contract unchanged).

## Approach

`feature-branch-chain` from change-16 on `feature/change-18-architecture-cleanup`.

| # | PR | Budget | Anchor |
|---|---|---|---|
| 1 | `golangci-lint-v2-baseline` | ≤ 400 | `.golangci.yml` (v2) + CI workflow |
| 2 | `ports-dtos-extract` | ≤ 200 | DTOs out of `ports.go` |
| 3 | `postgres-repos-split` | ≤ 400 | per-UoW files; preserve `db.go` boundary |
| 4 | `object-debate-split` | ≤ 400 | per use case + shared helpers |
| 5 | `telegram-handler-refactor` | ≤ 400 | `New(Config)` + viewmodel render split |
| 6 | `main-runtime-extract` | ≤ 400 | `internal/runtime/` composition + shutdown |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Linter surfaces hundreds of existing issues | High | baseline ignores in `.golangci.yml`; 3-phase removal plan |
| `NewHandler*` refactor (PR5) breaks tests | Med | private `newHandlerWithStore` test seam |
| Repo split (PR3) loses `WithinIngestionTx` wiring | Med | same tx path; integration tests stay green |
| Adapter render (PR5) leaks `models.InlineKeyboardMarkup` into `app/` | Med | app returns `[]BacklogViewItem{Actions []BacklogAction}` viewmodel; telegram adapter converts |

## Rollback

Per PR additive. Each reverts with `git revert` against its chain base. The chain merges at any prefix; the feature branch never breaks `main`.

## Dependencies

- `internal/postgres/db.go` `WithinIngestionTx` UoW boundary (existing).
- `internal/runtime/` package created in PR6 (does not exist yet).

## Success criteria

- [ ] All 6 PRs merged in chain order.
- [ ] `go test -short ./...`, `go vet ./...`, `gofmt -l`, `golangci-lint run` green.
- [ ] `handler.go` ≤ 350 LOC, single `New(Config)`; `ports.go` ≤ 80 LOC; `repositories.go` deleted; `main.go` ≤ 60 LOC.
- [ ] ROADMAP.md updated; change archived.
- [ ] Linter v2 active in CI; baseline ignores documented.
