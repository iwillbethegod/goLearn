# Strategy Pattern Implementation for Repository Selection

This document explains how the Strategy Pattern is implemented in goLearn for flexible repository dependency injection.

## Overview

The Strategy Pattern allows the application to select different repository implementations (strategies) at runtime without changing the business logic code.

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        cmd/server/main.go                       │
│                        (CLI Entrypoint)                         │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             │ uses
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                 internal/app/factory.go                         │
│                   (Factory Pattern)                             │
│  - NewRepository(cfg)                                           │
│  - Decides which strategy to instantiate                        │
└────────┬──────────────┬──────────────┬───────────────────────────┘
         │              │              │
         │ creates      │ creates      │ creates
         ▼              ▼              ▼
    ┌─────────┐  ┌──────────┐  ┌──────────┐
    │ Memory  │  │ JSONFile │  │ Postgres │
    │Strategy │  │Strategy  │  │Strategy  │
    └─────────┘  └──────────┘  └──────────┘
         │              │              │
         └──────────────┼──────────────┘
                        │ all implement
                        ▼
              ┌─────────────────────┐
              │  user.Repository    │
              │    (Interface)      │
              └─────────────────────┘
                        ▲
                        │ injected into
                        │
              ┌─────────────────────┐
              │  user.Service       │
              │  (Business Logic)   │
              └─────────────────────┘
```

## Key Components

### 1. Repository Interface (`internal/user/repository.go`)

Defines the contract for all repository implementations:

```go
type Repository interface {
    Add(User) error
    List() ([]User, error)
}
```

**This is the abstraction that all strategies must implement.**

### 2. Repository Implementations (Strategies)

Located in `internal/storage/`:

#### Memory (`internal/storage/memory/user_repo.go`)
- Ephemeral storage (lost when process ends)
- Useful for testing and demos
- In-process mutex-based synchronization

#### JSONFile (`internal/storage/jsonfile/user_repo.go`)
- Persistent storage to a JSON file
- Data survives process restarts
- Used by default in the CLI

#### Postgres (`internal/storage/postgres/user_repo.go`)
- Database-backed storage (stub for future)
- Would be used for production deployments

**Each implementation satisfies the `Repository` interface.**

### 3. Factory Pattern (`internal/app/factory.go`)

Creates the appropriate repository based on configuration:

```go
func NewRepository(cfg RepositoryConfig) (user.Repository, error) {
    switch cfg.Type {
    case TypeMemory:
        return memory.NewUserRepo(cfg.Logger), nil
    case TypeJSONFile:
        return jsonfile.NewUserRepo(cfg.JSONPath, cfg.Logger)
    case TypePostgres:
        return nil, fmt.Errorf("postgres repository not yet implemented")
    default:
        return nil, fmt.Errorf("unknown repository type: %s", cfg.Type)
    }
}
```

**This is the single place where concrete repository types are instantiated.**

### 4. Dependency Injection

In `cmd/server/main.go`:

```go
cfg := app.RepositoryConfig{
    Type:     app.RepositoryType(*storageType),  // "memory" or "jsonfile"
    JSONPath: *dataPath,
    Logger:   log,
}
repository, err := app.NewRepository(cfg)
service := user.NewService(repository, log, metricsCollector)
```

The service receives the repository it needs via dependency injection.

## Adding a New Repository Strategy

To add a new storage strategy (e.g., MongoDB):

### Step 1: Create the Implementation

Create `internal/storage/mongodb/user_repo.go`:

```go
package mongodb

import (
    "log/slog"
    "github.com/ashishsinghbhadoria/goLearn/internal/user"
)

type UserRepo struct {
    client *mongo.Client
    logger *slog.Logger
}

func NewUserRepo(client *mongo.Client, logger *slog.Logger) *UserRepo {
    return &UserRepo{client: client, logger: logger}
}

func (r *UserRepo) Add(u user.User) error {
    // implementation
}

func (r *UserRepo) List() ([]user.User, error) {
    // implementation
}

var _ user.Repository = (*UserRepo)(nil)  // Verify interface compliance
```

### Step 2: Update the Factory

In `internal/app/factory.go`:

```go
import "github.com/ashishsinghbhadoria/goLearn/internal/storage/mongodb"

const TypeMongoDB RepositoryType = "mongodb"

func NewRepository(cfg RepositoryConfig) (user.Repository, error) {
    switch cfg.Type {
    case TypeMemory:
        // existing case
    case TypeJSONFile:
        // existing case
    case TypeMongoDB:
        client := mongodb.Connect(cfg.MongoURI)  // example
        return mongodb.NewUserRepo(client, cfg.Logger), nil
    // ...
    }
}
```

### Step 3: Update the CLI

In `cmd/server/main.go`:

```go
storageType := flag.String("storage", "jsonfile", "Storage strategy (memory, jsonfile, mongodb)")
```

### Step 4: Add Tests

In `internal/app/factory_test.go` or a separate test file:

```go
func TestMongoDBStrategy(t *testing.T) {
    log := logger.NewLogger()
    repo, err := NewRepository(RepositoryConfig{
        Type:      TypeMongoDB,
        MongoURI:  "mongodb://localhost:27017",
        Logger:    log,
    })
    // ... test assertions
}
```

## Benefits of This Approach

| Benefit | How It Helps |
|---------|-------------|
| **No business logic changes** | Add/swap storage without touching service.go |
| **Easy testing** | Inject in-memory repository in tests |
| **Runtime flexibility** | Select strategy via CLI flags or environment variables |
| **Maintainability** | Each strategy is isolated in its own package |
| **Scalability** | Add new strategies without refactoring existing code |
| **Dependency inversion** | High-level code depends on abstractions, not concrete types |

## Testing Different Strategies

```bash
# Test with in-memory (ephemeral)
go run ./cmd/server --storage=memory --name="Alice" --email="alice@example.com"

# Test with JSON file (persistent)
go run ./cmd/server --storage=jsonfile --name="Bob" --email="bob@example.com"
go run ./cmd/server --storage=jsonfile --list

# Run all tests (includes factory tests)
go test ./...
```

## Future Extensions

With this pattern, it's trivial to add:
- **Redis caching layer**: `internal/storage/redis/`
- **S3 storage**: `internal/storage/s3/`
- **DynamoDB**: `internal/storage/dynamodb/`
- **Cached composite**: `internal/storage/cached/` (JSONFile + Redis)

Each can be a separate package, tested independently, and plugged in via the factory.
