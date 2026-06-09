# http-ingest-api Specification

HTTP adapter layer exposing text ingestion over REST. Two endpoints, Go stdlib `net/http` with Go 1.22 ServeMux, zero new dependencies.

## Requirements

### Requirement: Text Ingestion Endpoint

The system SHALL expose `POST /v1/ingest-text` accepting `IngestTextRequest` JSON and returning `IngestTextResult` JSON.

#### Scenario: Successful ingestion

- GIVEN a valid `IngestTextRequest` with `workspace_id`, `content`, `source`, and `object`
- WHEN a client sends `POST /v1/ingest-text`
- THEN the system returns `201 Created` with `IngestTextResult` JSON containing `source_id`, `object_id`, `audit_event_id`, `content_checksum`, `identity_key`, and `duplicate`
- AND the `Content-Type` response header is `application/json`

#### Scenario: Duplicate ingestion

- GIVEN an identical request with the same `idempotency_key`
- WHEN a client sends `POST /v1/ingest-text`
- THEN the system returns `201 Created` with `duplicate: true` and the existing resource IDs
- AND no new knowledge artifact is persisted

#### Scenario: Missing required field

- GIVEN a request missing `workspace_id` or `content`
- WHEN a client sends `POST /v1/ingest-text`
- THEN the system returns `400 Bad Request` with error JSON

#### Scenario: Not found reference

- GIVEN a request referencing a workspace that does not exist
- WHEN a client sends `POST /v1/ingest-text`
- THEN the system returns `404 Not Found` with error JSON

#### Scenario: Internal failure

- GIVEN the persistence layer returns an unexpected error
- WHEN a client sends `POST /v1/ingest-text`
- THEN the system returns `500 Internal Server Error` with error JSON
- AND no internal detail (stack trace, DSN) is leaked

### Requirement: Health Probe

The system SHALL expose `GET /v1/health` as a liveness probe.

#### Scenario: Server is running

- GIVEN the HTTP server is up
- WHEN a client sends `GET /v1/health`
- THEN the system returns `200 OK` with body `{"status":"ok"}`

### Requirement: Error Response Shape

All error responses SHALL use a consistent JSON structure.

#### Scenario: Error body format

- GIVEN any request that results in a 4xx or 5xx status
- WHEN the error response is returned
- THEN the body contains `error` (short label), `message` (human-readable), and `code` (machine-readable constant)

| Status | Error Code | Trigger |
|--------|-----------|---------|
| 400 | `VALIDATION_ERROR` | `ErrValidation` from domain |
| 404 | `NOT_FOUND` | `ErrNotFound` from domain |
| 500 | `INTERNAL_ERROR` | Any other error |

### Requirement: Composition Root

`cmd/api/main.go` SHALL wire the full request path from config to running server.

#### Scenario: PostgreSQL mode

- GIVEN `DATABASE_DSN` environment variable is set
- WHEN the application starts
- THEN it opens a PostgreSQL connection pool, constructs `IngestTextService` with the real repository, and registers routes on the ServeMux

#### Scenario: In-memory mode

- GIVEN `DATABASE_DSN` environment variable is empty
- WHEN the application starts
- THEN it constructs `IngestTextService` with an in-memory fake unit of work and registers routes on the ServeMux

#### Scenario: Port configuration

- GIVEN the application starts
- WHEN no `PORT` override is set
- THEN the server listens on port `8080`

### Requirement: Graceful Shutdown

The system SHALL handle SIGINT and SIGTERM by draining in-flight requests before exit.

#### Scenario: Signal received

- GIVEN the HTTP server is serving requests
- WHEN SIGINT or SIGTERM is received
- THEN the system stops accepting new connections, waits up to a configurable timeout for in-flight requests, and exits cleanly
