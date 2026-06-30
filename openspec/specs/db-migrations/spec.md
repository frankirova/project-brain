# Database Migrations Specification

## Purpose

Define the schema-lifecycle contract: the API binary MUST apply pending migrations automatically, in a versioned, idempotent, fail-fast manner, before serving traffic. Per-migration content is out of scope. Migrations ship with the binary; deployment docs cover packaging.

## Requirements

### Requirement: Auto-Apply on Startup

The system MUST apply all pending migrations before the API begins serving any request.

#### Scenario: Pending migrations applied before traffic

- GIVEN on-disk migrations include versions newer than the database's highest recorded version
- WHEN the API binary starts
- THEN every pending version is applied
- AND no HTTP request is accepted before the step completes

### Requirement: Ordered Versioning

The system MUST assign each migration a unique ordered version and MUST apply them in strictly ascending order.

#### Scenario: Versions applied in ascending order

- GIVEN on-disk versions V1, V2, V3 with V1 < V2 < V3
- WHEN the runner is invoked
- THEN it applies V1, then V2, then V3, in that exact order

### Requirement: Version-Table State Tracking

The system MUST persist every applied version in a version-tracking table in the same database, and MUST NOT re-apply a recorded version.

#### Scenario: Applied version is recorded and not replayed

- GIVEN the runner successfully applies version 7
- WHEN a subsequent startup runs with the same on-disk set
- THEN version 7 is recorded in the version-tracking table
- AND the runner does not execute version 7's SQL again

### Requirement: Idempotent Startup

The system MUST treat an up-to-date database as a no-op.

#### Scenario: Up-to-date database is a no-op

- GIVEN the database's highest recorded version equals the highest on-disk version
- WHEN the API binary starts
- THEN the runner performs no DDL and returns success without error

### Requirement: Fail-Fast on Migration Error

The system MUST exit non-zero and MUST NOT serve traffic if any migration step fails. The failure MUST be diagnosable from a structured log that names the failing version.

#### Scenario: Migration failure aborts startup

- GIVEN a migration file with version 5 contains invalid SQL
- WHEN the API binary starts
- THEN the process exits with a non-zero status
- AND the API does not begin serving traffic
- AND a structured log entry names version 5 as the failing version

### Requirement: Baselining Existing Databases

The system MUST recognize a database with the schema sentinel present but no version records, and MUST mark all on-disk migrations applied without re-executing their SQL. The baselining action MUST be logged.

#### Scenario: Sentinel present, no records — baselined

- GIVEN the database contains the schema sentinel
- AND the version-tracking table is empty
- WHEN the API binary starts
- THEN every on-disk version is recorded in the version-tracking table
- AND no migration SQL is executed
- AND a structured log records the baselining action

#### Scenario: Sentinel absent, no records — fresh install

- GIVEN the database does NOT contain the schema sentinel
- AND the version-tracking table is empty
- WHEN the API binary starts
- THEN the runner applies every on-disk migration in ascending order

### Requirement: Delta-Only Updates After Baseline

The system MUST apply only versions strictly greater than the current maximum recorded version.

#### Scenario: New migration applied on next start

- GIVEN the database's highest recorded version is 7
- AND a new on-disk migration with version 8 is present
- WHEN the API binary starts
- THEN the runner applies version 8 only
- AND version 8 is recorded

#### Scenario: Versions at or below max are skipped

- GIVEN the database's highest recorded version is 7
- AND on-disk versions include 5, 6, 7, and 8
- WHEN the API binary starts
- THEN versions 5, 6, and 7 are NOT re-applied
- AND only version 8 is applied
