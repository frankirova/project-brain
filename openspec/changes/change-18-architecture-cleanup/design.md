# Design: change-18 — architecture cleanup

## Technical approach

Six-smell hexagonal refactor on `feature/change-18-architecture-cleanup` as a 6-PR `feature-branch-chain` (each ≤ 400 lines, reverts cleanly). PR1 lands `.golangci.yml` (v2.12.x, `version: "2"`, per #1741/#1742) + CI; PR2–PR6 are pure file reorganization + constructor cleanup, no domain behavior change.

Spec → PR map: PR1 → `repo-quality` (NEW); PR2 + PR5 → `telegram-bot-adapter` (PR2 lifts DTOs, PR5 collapses 7 `NewHandler*` to one `New(Config)` per #1736 + viewmodel/render split per #1737); PR3 → `knowledge-core-ingestion` (preserves `WithinIngestionTx` in `db.go` per #1754); PR4 → no spec (internal split); PR6 → `http-api-runtime` (NEW, composition root).

## Architecture decisions

| # | Choice | Alternatives | Rationale | Tradeoff |
|---|--------|--------------|-----------|----------|
| 1 | **PR5 constructor**: `New(Config) (*Handler, error)` + private `newHandlerWithStore` seam | (a) 7 overloads, (b) functional options, (c) builder | Per #1736: Config struct is the 2025 idiom for hexagonal services with 10+ ports + cross-field invariants; functional options win for libraries, not app services. | Tests build a Config; `Validate()` makes it explicit |
| 2 | **PR5 render split**: app returns `[]BacklogViewItem{Actions []BacklogAction}`; telegram converts to `models.InlineKeyboardMarkup` | (a) `models.*` in app leaks SDK, (b) template in adapter | Per #1737: app stays UI-agnostic; SDK types never cross `internal/app/`. | One more type; payoff is testable app-side rendering |
| 3 | **PR3 repo split**: per-UoW files (`ingestion_repo.go`, `object_validation_repo.go`, `object_debate_repo.go`, `audit_event_repo.go`); `WithinIngestionTx` stays in `db.go`; `relation_repository.go` + `backlog_query.go` lifted out. | (a) per-table (6 already exist per #1737), (b) keep mega-file | Per #1754: UoW is the natural cut; `db.go` is the only `BeginTx`/`Commit`/`Rollback` call site. | 3 small bundles replace 1 mega; each ≤ 200 LOC |
| 4 | **PR1 linter rollout**: 3-phase baseline-ignore removal — P1 drops `internal/app/`+`cmd/`; P2 drops `internal/telegram/`+`internal/postgres/repositories.go`; P3 clears rest | (a) fix all in PR1 (blocks chain), (b) no linter | Per #1742: 21k LOC with hundreds of existing violations cannot block the refactor; linter stays on as a tripwire for new code while debt is paid down per smell. | ~3 follow-up PRs |
| 5 | **PR4 service split**: per-use-case files (`object_debate_backlog.go`, `_validate.go`, `_discard.go`) + `_helpers.go` for 3 helpers | (a) keep 525-LOC monolith, (b) full SRP per method | 4 use cases × ~120 lines fits the budget; 3 helpers (status guards, metadata) span use cases. | Helpers file is non-use-case; `object_debate_` prefix keeps namespace coherent |
| 6 | **PR6 runtime extraction**: `internal/runtime/` (created by PR6) with `runtime.go` + `telegram_runtime.go` + `shutdown.go` | (a) package per binary, (b) keep `cmd/api/main.go` as root | Per #1737: composition root is one concept (how the app boots), not per-binary. One package, one ≤60-LOC sequencer. | First appearance; future binaries share it |

## Data flow

**Ingest (PR3 — split internals, contract unchanged)**: `HTTP → httpapi → app.IngestTextService.Ingest → WithinIngestionTx (db.go) → ingestion_repo.go: Sources/KnowledgeObjects/ObjectSources.Create + audit_event_repo.go: AuditEvents.Create → PG (4 writes)`. Tx boundary preserved (#1754).

**Telegram render (PR5 — NEW)**: `app.ListHumanBacklog → BacklogPage{Items} → handler_render.go: []BacklogViewItem{Actions []BacklogAction} (app viewmodel) → handler_render_telegram.go: → models.InlineKeyboardMarkup (adapter render) → Sender`.

**Composition (PR6)**: `main.go (≤60 LOC) → config.Load → enforceProductionAuth → runtime.BuildServices/BuildHandlers/BuildTelegramBot → runtime.RunShutdown`. Telegram is a peer to HTTP; `main.go` does not import `internal/telegram`.

## File changes

| PR | File | Action |
|----|------|--------|
| 1 | `.golangci.yml`, `.github/workflows/lint.yml` | Create — v2 schema + CI; `Makefile` Modify — `lint` target |
| 2 | `internal/telegram/dto.go`, `internal/embeddings/dto.go` | Create — DTOs; `internal/app/ports.go` Modify — ≤ 80 LOC |
| 3 | `internal/postgres/{ingestion,object_validation,object_debate,audit_event}_repo.go` | Create — per-UoW repos + shared audit |
| 3 | `internal/postgres/{relation_repository,backlog_query}.go` | Create — lift standalones; `repositories.go` Delete |
| 4 | `internal/app/object_debate_{backlog,validate,discard,helpers}.go` | Create; `object_debate.go` Delete |
| 5 | `internal/telegram/handler.go` | Modify — ≤ 350 LOC; `New(Config)` |
| 5 | `internal/telegram/{handler_render,handler_render_telegram,config}.go` | Create — viewmodel + render + `Config` |
| 6 | `internal/runtime/{runtime,telegram_runtime,shutdown}.go` | Create; `cmd/api/main.go` Modify — ≤ 60 LOC sequencer |

## Interfaces / contracts

```go
// PR5 — Config struct + private seam (per #1736)
type Config struct { Service *app.IngestTextService; Detector collisionChecker; RawInputs app.RawInputRepository; Sender Sender; PendingStore pendingStore; Backlog backlogLister; Finder app.KnowledgeObjectFinder; ReviewStore reviewActionStore; Validator reviewValidator; Debater reviewDebator; NewToken func() string; Logger *slog.Logger }
func (c *Config) Validate() error
func (c *Config) applyDefaults()
func New(c Config) (*Handler, error)
func newHandlerWithStore(c Config) *Handler  // package-private seam

// PR5 — viewmodel (the new boundary, in internal/telegram/dto.go)
type BacklogViewItem struct { ID uuid.UUID; Title, Summary, Status string; Actions []BacklogAction }
type BacklogAction struct { Label, Token string }

// PR6 — composition root (main.go telegram-agnostic)
type TelegramBot struct{}
func BuildTelegramBot(ctx context.Context, cfg *config.Config, svc *app.IngestTextService, backlog *app.ObjectDebateService, validate *app.ValidateObjectService, rawInputs app.RawInputRepository, pending app.PendingValidationStore, review app.TelegramReviewActionStore, finder app.KnowledgeObjectFinder, detector *app.CollisionDetector, logger *slog.Logger) (*TelegramBot, error)
func RunShutdown(ctx context.Context, server *http.Server, bot *TelegramBot, retryDone <-chan struct{}, timeout time.Duration, logger *slog.Logger)

// PR3 — UoW boundary preserved verbatim (per #1754)
func (db *DB) WithinIngestionTx(ctx context.Context, fn func(context.Context, app.IngestionRepositories) error) error
```

## Testing strategy

| Layer | What to test | Approach |
|-------|--------------|----------|
| Unit | `Config.Validate()` cross-field (`Backlog!=nil ⇒ Finder!=nil`); `newHandlerWithStore` ≡ the 7 old ctors | table-driven; golden struct |
| Unit | per-UoW repo (3 files); per-use-case service (4 files) | table-driven + change-17 fakes |
| Unit | `handler_render.go` emits expected viewmodel | snapshot |
| Integration | `WithinIngestionTx` rollback (4-write contract); `go test ./internal/postgres/...` | keep change-17 PR1 test green; run as-is |
| Lint | `make lint` exits 0; `config verify` accepts v2 | new CI job in PR1 |
| E2E | none new; change-16/17 smoke stays green | reuse |

## Migration / rollout

**Linter 3-phase rollout** (gated by PR order): P1 lands with PR1 ignoring `internal/app/`+`cmd/`; P2 runs after PR2 dropping `internal/telegram/`+`internal/postgres/repositories.go`; P3 runs after PR3+PR4+PR5 clearing the rest. Each phase is a follow-up PR off `main`, recorded in `ROADMAP.md` per `repo-quality`. The chain merges at any prefix — PRs 1–3 ship while 4–6 are reviewed.

**No data migration, no feature flags.** All 6 PRs are additive file moves + constructor refactor; no migrations, no API change, no env var rename. `cmd/api/main_test.go` from change-16 PR1 must pass unchanged through PR6 (per `http-api-runtime` startup-invariant requirement).

## Open questions

- **Test seam visibility**: keep `newHandlerWithStore` lowercase in `handler.go` (standard Go: same-package `_test.go` calls it directly). The `telegram-bot-adapter` spec scenario "via an exported test wrapper" implies a different shape — design drops that requirement and uses the standard private-seam idiom. **Needs user confirmation** that the spec scenario can be relaxed.
- **`internal/runtime/` flat vs split**: stay flat for change-18; revisit `http_runtime.go` vs `telegram_runtime.go` split as a follow-up once a third binary lands. Design resolves: stay flat.
