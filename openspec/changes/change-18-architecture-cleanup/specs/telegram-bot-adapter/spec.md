# Delta for Telegram Bot Adapter

## ADDED Requirements

### Requirement: DTOs live in adapter packages

Telegram DTOs (callback data, viewmodel items, action identifiers) MUST live in `internal/telegram/dto.go`. Embedding DTOs MUST live in `internal/embeddings/dto.go`. `internal/app/ports.go` MUST NOT import any `internal/telegram` or `internal/embeddings` type. (#1737)

#### Scenario: app and telegram stay decoupled

- GIVEN the codebase compiles
- WHEN `go list -deps ./internal/app` runs
- THEN the list MUST NOT contain `internal/telegram` or `internal/embeddings`

#### Scenario: DTOs are co-located with the adapter

- GIVEN a `BacklogCallback` type exists
- WHEN a developer greps for its definition
- THEN it is declared in `internal/telegram/dto.go` and not in `internal/app/ports.go`

### Requirement: single public constructor with Config

The handler MUST be constructible through exactly one public entry point: `New(Config) (*Handler, error)`. The `Config` MUST expose a private `newHandlerWithStore(...)` test seam. The 4 public + 3 private `NewHandler*` overloads MUST NOT exist after the change. (#1736)

#### Scenario: Production path uses `New`

- GIVEN the handler is wired at startup
- WHEN it is constructed
- THEN the call site invokes `telegram.New(cfg)` and receives `(*Handler, error)`

#### Scenario: Test path uses the private seam

- GIVEN a unit test needs an in-memory store
- WHEN the test builds the handler
- THEN it calls the private `newHandlerWithStore(...)` seam via an exported test wrapper

#### Scenario: Legacy overloads are gone

- GIVEN the refactor is complete
- WHEN `rg "func NewHandler" internal/telegram` runs
- THEN no matches are returned and `rg "func New\(" internal/telegram` returns exactly one match

### Requirement: render split across files

`internal/telegram/handler.go` MUST be ≤ 350 lines. Render code MUST be split across at least two files: `handler_render.go` (viewmodel assembly) and `handler_render_telegram.go` (inline-keyboard conversion). The handler MUST return app-level viewmodel types; conversion to `models.InlineKeyboardMarkup` MUST NOT occur inside `internal/app/`. (#1737)

#### Scenario: handler.go stays under the line budget

- GIVEN the refactor is complete
- WHEN `wc -l internal/telegram/handler.go` runs
- THEN the count is ≤ 350

#### Scenario: render lives in dedicated files

- GIVEN the handler emits a backlog view
- WHEN a developer greps `models.InlineKeyboardMarkup` under `internal/telegram`
- THEN every match is in `handler_render_telegram.go` and zero are in `handler_render.go`

#### Scenario: app layer is render-free

- GIVEN the handler builds a backlog response
- WHEN a developer greps `internal/app` for `InlineKeyboardMarkup`
- THEN no matches are returned

## MODIFIED Requirements

### Requirement: Configuration

The system SHALL read the bot token from `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN`, operate in polling mode, and be constructed via `New(Config) (*Handler, error)`.
(Previously: any of 7 `NewHandler*` overloads could construct the handler; construction is now restricted to a single public `New(Config)`, per #1736.)

#### Scenario: Missing bot token

- GIVEN `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` is not set
- WHEN the application starts
- THEN the adapter logs an error, does not start polling, and the HTTP server keeps running

#### Scenario: Bot token configured

- GIVEN `PROJECT_BRAIN_TELEGRAM_BOT_TOKEN` is set
- WHEN the application starts
- THEN the adapter starts polling Telegram and the handler is constructed via `telegram.New(cfg)` exactly once
