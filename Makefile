# =============================================================================
# project-brain — operational Makefile
# =============================================================================
# This file is INTENTIONALLY MINIMAL. The repo is bootstrapping
# `.golangci.yml` (v2 schema) and a `make lint` target as part of
# change-18 PR1. The pre-existing `.github/workflows/ci.yml` (added by
# change-16 PR5) runs gofmt + vet + short tests; `make lint` is the LOCAL
# equivalent of the new `lint.yml` workflow — same config, same timeout.
#
# This Makefile is NOT meant to replace a future expanded build harness
# (image targets, coverage reports, etc.); it only covers the local
# parity requirements spelled out in `repo-quality` §"local parity via
# Make":
#   - `make lint` MUST pass `-c .golangci.yml` and `--timeout >= 5m`
#   - `make lint` MUST exit 0 when the tree is clean
#   - `make lint` MUST exit non-zero when a violation is found
# =============================================================================

# Always use bash so recipes work the same on Linux and macOS runners
# (the project already has Git Bash on Windows dev machines).
SHELL := /bin/bash

# A `.PHONY` declaration is mandatory for any target that does NOT produce
# a file of the same name; otherwise `make` will re-check the filesystem
# on every invocation.
.PHONY: help lint

# `help` is the default goal and prints a short usage block. Keeping it
# here means new contributors can run `make` with no arguments and see
# the available targets.
help:
	@echo "project-brain — make targets"
	@echo "  make lint   run golangci-lint v2 against the tree (matches CI)"

# -----------------------------------------------------------------------------
# lint — runs golangci-lint against the working tree using the v2 config.
# -----------------------------------------------------------------------------
# The two flags are non-negotiable:
#   `-c .golangci.yml`   uses the same config the CI job uses
#                        (see `.github/workflows/lint.yml`).
#   `--timeout=5m`       matches the 5m budget in the config + CI step.
# Locally you can override the timeout with `make lint LINT_TIMEOUT=10m`.
# -----------------------------------------------------------------------------
LINT_TIMEOUT ?= 5m

lint:
	golangci-lint run -c .golangci.yml --timeout=$(LINT_TIMEOUT)
