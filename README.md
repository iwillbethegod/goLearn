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

Add a user:

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

## Architecture

The project follows Clean Architecture principles:

- Handlers and CLI code are entry points
- `internal/user` contains business logic and abstractions
- Repositories are implemented separately from the service layer
- The CLI uses `internal/storage/jsonfile` so users remain available between separate command runs
- Shared infrastructure such as logging and metrics are provided via `pkg`
- `internal/transport/grpc` and `internal/storage/postgres` are prepared for future extensions
