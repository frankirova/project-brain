# Hermes Agents

Hermes Agents currently contains the Project Brain backend scaffold used by the SDD `project-brain-mvp` change.

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
| `PROJECT_BRAIN_DATABASE_DSN` | empty | Optional until the PostgreSQL persistence work unit is implemented. |

PostgreSQL verification commands will be added with the persistence work unit. This scaffold intentionally does not implement ingestion domain logic or database repositories yet.
