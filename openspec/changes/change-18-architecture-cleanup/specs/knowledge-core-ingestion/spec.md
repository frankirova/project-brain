# Delta for Knowledge Core Ingestion

## ADDED Requirements

### Requirement: repository files split by Unit of Work

The 528-LOC `internal/postgres/repositories.go` MUST be replaced with per-UoW files (e.g., `object_relations_repo.go`, `sdd_documents_repo.go`, `raw_inputs_repo.go`). The `WithinIngestionTx` boundary in `internal/postgres/db.go` MUST be preserved verbatim. The legacy `repositories.go` MUST be deleted. (#1754)

#### Scenario: per-UoW files exist

- GIVEN the split is complete
- WHEN a developer lists `internal/postgres/*.go`
- THEN at least three `<table>_repo.go` files are present and `repositories.go` is absent

#### Scenario: UoW boundary is preserved

- GIVEN the split is complete
- WHEN `rg "WithinIngestionTx" internal/postgres` runs
- THEN exactly one match exists, in `db.go`, with the same signature

#### Scenario: postgres tests stay green

- GIVEN the split is complete
- WHEN `go test ./internal/postgres/...` runs
- THEN the suite passes unchanged, including the UoW integration test

### Requirement: no behavior change to the ingestion contract

The 4-write contract (idempotency + audit) MUST be unchanged. No new migrations MAY be introduced. No new public methods MAY be added to any repository interface.

#### Scenario: change-17 PR1 test still passes

- GIVEN the change-17 PR1 test asserts the 4-write contract
- WHEN it runs against the refactored code
- THEN it passes unchanged and exactly 4 application writes occur per accepted ingestion

#### Scenario: no new public repository methods

- GIVEN the split is complete
- WHEN a developer greps `internal/postgres` for exported method declarations
- THEN the method-name set matches the pre-change set

## MODIFIED Requirements

### Requirement: Create Auditable Knowledge Records

The system MUST persist one Source, one KnowledgeObject, one Source-to-object link, and one audit event per accepted ingestion, using methods in per-table `<table>_repo.go` files that participate in the `WithinIngestionTx` UoW boundary.
(Previously: all methods lived in a single 528-LOC `repositories.go`; the layout is now per-UoW, the transaction boundary is unchanged, and the 4-write contract is preserved.)

#### Scenario: New text creates complete records

- GIVEN a valid text ingestion request that has not been ingested before
- WHEN the request is accepted
- THEN a Source, a KnowledgeObject, a link, and an audit event are all created

#### Scenario: Persistence is all-or-nothing

- GIVEN a valid text ingestion request
- WHEN any required record cannot be persisted
- THEN the ingestion fails, no partial records remain, and rollback is driven by `WithinIngestionTx` (not ad-hoc compensating writes)

### Requirement: per-table file size

Each `<table>_repo.go` file MUST be ≤ 200 lines. Splitting MUST follow UoW boundaries already encoded by `WithinIngestionTx`.
(Previously: no per-file size constraint existed; all table repos lived in a single 528-LOC `repositories.go`.)

#### Scenario: every per-table file fits the budget

- GIVEN the split is complete
- WHEN `wc -l internal/postgres/*_repo.go` runs
- THEN every line count is ≤ 200

#### Scenario: a file approaching the cap is split, not padded

- GIVEN a per-table repo would exceed 200 lines as one file
- WHEN the developer splits it
- THEN a new `<related_table>_repo.go` is introduced, the original file stays ≤ 200 lines, and the split respects `WithinIngestionTx`
