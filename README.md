# Project Brain

Project Brain currently contains the backend foundation for the SDD `project-brain-mvp` change.

## Backend scaffold

Run the Go test suite:

```sh
go test ./...
```

Run the minimal API scaffold:

```sh
go run ./cmd/api
```

Configuration is read from environment variables:

| Variable | Default | Notes |
|---|---:|---|
| `PROJECT_BRAIN_ENV` | `development` | Runtime environment label. |
| `PROJECT_BRAIN_API_PORT` | `8080` | Validated TCP port for future API binding. |
| `PROJECT_BRAIN_DATABASE_DSN` | empty | PostgreSQL DSN for application runtime wiring. |
| `PROJECT_BRAIN_TEST_DATABASE_DSN` | empty | Enables PostgreSQL integration tests when set. |

## PostgreSQL verification

The default test command runs all unit tests and skips PostgreSQL integration tests when no test DSN is configured:

```sh
go test ./...
```

To run the PostgreSQL integration tests, create a disposable database, apply the migration, and provide its DSN:

```sh
PROJECT_BRAIN_TEST_DATABASE_DSN="postgres://user:password@localhost:5432/project_brain_test?sslmode=disable" go test ./internal/postgres -v
```

The integration tests verify ingestion persistence, duplicate handling, and rollback behavior.
