# Repo Quality Specification

## Purpose

Establish a v2 golangci-lint baseline for the project-brain Go codebase (~21k LOC) with a 3-phase rollout. (#1741 / #1742; v2.12.x, `version: "2"`, released 2025-03-24)

## Requirements

### Requirement: golangci-lint v2 active in CI

The repository MUST run `golangci-lint run` in `.github/workflows/lint.yml` using v2.x. `.golangci.yml` MUST declare `version: "2"`. A violation MUST fail CI with a non-zero exit code.

#### Scenario: CI job uses a v2.x action

- GIVEN `.github/workflows/lint.yml` is present
- WHEN the workflow runs
- THEN it invokes `golangci/golangci-lint-action` pinned to a v2.x tag with `--config .golangci.yml`

#### Scenario: Config is valid v2 schema

- GIVEN `.golangci.yml` exists
- WHEN a developer runs `golangci-lint config verify`
- THEN the file is accepted as valid v2 schema

#### Scenario: Violation fails the build

- GIVEN a Go file contains a violation
- WHEN `golangci-lint run` runs in CI
- THEN the process exits non-zero and the lint job is reported failed

### Requirement: baseline ignores for existing debt

`.golangci.yml` MUST declare `issues.exclude-rules` silencing pre-existing violations in exactly 6 paths: `internal/app/`, `internal/telegram/`, `internal/postgres/repositories.go`, `internal/postgres/migrations.go`, `internal/runtime/`, `cmd/`. The baseline MUST be removed in 3 phases, each removing 2 paths.

#### Scenario: Baseline silences enumerated paths

- GIVEN the baseline block is in place
- WHEN a developer adds a violation inside one of the 6 paths
- THEN `golangci-lint run` reports zero issues for that path

#### Scenario: Phase 1 removes two paths

- GIVEN the baseline suppresses 6 paths
- WHEN phase 1 completes
- THEN exactly 2 paths are removed and a CHANGELOG or ROADMAP entry records the phase

#### Scenario: Final phase empties the baseline

- GIVEN phases 1 and 2 each removed 2 paths
- WHEN phase 3 completes
- THEN the baseline is empty and `golangci-lint run` reports zero issues on the full tree

### Requirement: linter rule selection

The enabled linter set MUST include the linters and thresholds below (v2 defaults per #1741 / #1742).

| Linter | Threshold |
|--------|-----------|
| `gofmt`, `govet`, `errcheck`, `gosimple`, `staticcheck`, `unused`, `ineffassign` | — |
| `funlen` | 60 / 40 |
| `gocognit` | 30 |
| `cyclop` | 10 |
| `lll` | 120 |
| `nestif` | 5 |

#### Scenario: All required linters are enabled

- GIVEN `.golangci.yml` is in place
- WHEN a developer runs `golangci-lint linters`
- THEN every linter in the table appears enabled

### Requirement: local parity via Make

`make lint` MUST run the same config as CI. The target MUST pass `-c .golangci.yml` and `--timeout` ≥ 5m.

#### Scenario: `make lint` exits 0 on a clean tree

- GIVEN no linter reports an issue
- WHEN a developer runs `make lint`
- THEN the command exits 0 with a "no issues" summary

#### Scenario: `make lint` fails on a new goimports violation

- GIVEN a Go file has misordered imports
- WHEN a developer runs `make lint`
- THEN the linter reports the file and line and the target exits non-zero
