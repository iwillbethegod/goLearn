# goLearn

A small Go project demonstrating Clean Architecture with a CLI-based user manager.

## Overview

This repository is structured with a clear separation of concerns:

- `cmd/server/main.go` — CLI entrypoint
- `internal/user` — business logic, models, and repository interface
- `internal/transport/grpc` — gRPC handler stub for future transport expansion
- `internal/storage/postgres` — Postgres repository stub
- `internal/storage/jsonfile` — JSON-file repository used by the CLI
- `internal/external/maps` — external service client stub
- `pkg/logger` — shared structured logging setup
- `pkg/metrics` — lightweight metrics collector

## Features

- Add users by name and email
- List users from persistent local JSON storage
- In-memory repository implementation for tests and examples
- Layered architecture with manual dependency injection
- Structured logging powered by `slog`

## Run

Add a user (uses JSONFile by default):

```bash
go run ./cmd/server --name="Alice" --email="alice@example.com"
```

List users:

```bash
go run ./cmd/server --list
```

Use a custom data file:

```bash
go run ./cmd/server --data=".data/users.json" --list
```

### Storage Strategies

The CLI supports different repository implementations via the `--storage` flag:

**In-Memory (ephemeral)** — useful for testing or demos:
```bash
go run ./cmd/server --storage=memory --name="Bob" --email="bob@example.com"
go run ./cmd/server --storage=memory --list  # Empty (no persistence)
```

**JSON File (persistent)** — default, data survives between CLI runs:
```bash
go run ./cmd/server --storage=jsonfile --name="Alice" --email="alice@example.com"
go run ./cmd/server --storage=jsonfile --list  # Data persists
```

## Architecture

The project follows Clean Architecture principles with the **Strategy Pattern** for repository selection:

- Handlers and CLI code are entry points
- `internal/user` contains business logic and the `Repository` interface (abstraction)
- `internal/storage/` holds all repository implementations (strategies):
  - `memory/` — ephemeral in-memory storage
  - `jsonfile/` — persistent JSON file storage
  - `postgres/` — stubbed for future implementation
- `internal/app/factory.go` implements the factory pattern to instantiate repositories
  - Decouples concrete implementations from the CLI code
  - Easy to swap strategies at runtime based on configuration
- Shared infrastructure (logging, metrics) is provided via `pkg/`
- `internal/transport/grpc` is prepared for future transport expansion

### Strategy Pattern Benefits

1. **Easy testing** — use in-memory repository in tests
2. **Runtime flexibility** — select storage via CLI flag
3. **Maintainability** — add new repositories without changing CLI code
4. **Dependency inversion** — CLI depends on the factory, not concrete types

