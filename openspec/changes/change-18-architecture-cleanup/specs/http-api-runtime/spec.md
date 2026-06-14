# HTTP API Runtime Specification

## Purpose

Define the composition root for the project-brain HTTP server and Telegram bot. The runtime package owns service construction, handler wiring, server lifecycle, and graceful shutdown. `cmd/api/main.go` becomes a sequencer that calls `runtime.Build*` in order.

## Requirements

### Requirement: composition root in `internal/runtime/`

The build path for the HTTP server (services, handlers, root mux, server) MUST live in `internal/runtime/runtime.go`. `cmd/api/main.go` MUST be ≤ 60 lines and MUST act as a sequencer calling `runtime.Build*` in order.

#### Scenario: main.go is a thin sequencer

- GIVEN the runtime is implemented
- WHEN `wc -l cmd/api/main.go` runs
- THEN the count is ≤ 60 and the file contains only `config.Load`, `enforceProductionAuth`, `runtime.Build*` calls, and `runtime.RunShutdown`

#### Scenario: services are built through runtime

- GIVEN `cmd/api/main.go` starts the HTTP server
- WHEN a developer greps main.go for service construction
- THEN no `New*Service(` or `New*Handler(` calls appear inline and all construction lives behind `runtime.Build*`

### Requirement: Telegram bot composition in `internal/runtime/`

Telegram bot construction (registry, update dispatcher, callback router) MUST live in `internal/runtime/telegram_runtime.go`. `cmd/api/main.go` MUST NOT import any `internal/telegram` type directly; it MUST call `runtime.BuildTelegramBot(...)` and receive an opaque `runtime.TelegramBot` value.

#### Scenario: main.go is telegram-agnostic

- GIVEN the runtime is implemented
- WHEN `rg "internal/telegram" cmd/api/main.go` runs
- THEN no matches are returned and the only telegram reference is the `runtime.BuildTelegramBot` call

#### Scenario: bot construction lives in runtime

- GIVEN the bot needs a registry, dispatcher, and callback router
- WHEN the developer greps `internal/runtime/telegram_runtime.go`
- THEN the file contains the construction sequence and returns a value whose type is defined in `internal/runtime`

### Requirement: shutdown coordination in `internal/runtime/`

Graceful shutdown logic (signal handling, `http.Server.Shutdown`, bot stop) MUST live in `internal/runtime/shutdown.go`. `main.go` MUST delegate shutdown to `runtime.RunShutdown(ctx, server, bot, logger)`.

#### Scenario: SIGTERM triggers ordered shutdown

- GIVEN the HTTP server and the bot are running
- WHEN the process receives SIGTERM
- THEN `runtime.RunShutdown` stops the bot first, calls `http.Server.Shutdown(ctx)` with a bounded grace period, and exits 0 once both have drained

#### Scenario: shutdown is not reimplemented in main

- GIVEN the runtime owns shutdown
- WHEN a developer greps `cmd/api/main.go` for `Shutdown` or `signal.Notify`
- THEN no matches are returned and all shutdown logic lives in `internal/runtime/shutdown.go`

### Requirement: startup invariants preserved

The `enforceProductionAuth` fail-closed check from change-16 PR1 MUST remain the first call after `config.Load()` in `main()`. The `cmd/api/main_test.go` invariant tests MUST pass unchanged after the refactor.

#### Scenario: auth check runs before any build step

- GIVEN the application starts in production mode with auth disabled
- WHEN `cmd/api/main.go` executes
- THEN `enforceProductionAuth` is the first call after `config.Load()` and the process exits non-zero before any `runtime.Build*` runs

#### Scenario: invariant tests pass unchanged

- GIVEN `cmd/api/main_test.go` from change-16 PR1 is in the tree
- WHEN `go test ./cmd/api/...` runs after the refactor
- THEN every invariant test passes without modification and no test file in `cmd/api/` was edited to compensate
