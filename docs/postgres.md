# Postgres backend (Day 5)

This page documents how to run goLearn against a real Postgres
instance — the third strategy registered in `internal/app/factory.go`,
selected by `-storage postgres`.

## Components

| File / dir | Purpose |
|---|---|
| `db/migrations/` | golang-migrate `.up.sql` / `.down.sql` files. Versioned (`000001_init.*`). Single source of truth for the schema; sqlc reads them too. |
| `db/queries/users.sql` | Hand-written SQL queries with sqlc directive comments (`-- name: AddUser :exec`). |
| `sqlc.yaml` | sqlc config. Engine `postgresql`, driver `pgx/v5`. Output → `internal/storage/postgres/pgdb/`. |
| `internal/storage/postgres/pgdb/` | sqlc output (committed). `db.go` `models.go` `users.sql.go`. Generated; do not edit. |
| `internal/storage/postgres/user_repo.go` | `user.Repository` impl. Wraps `pgxpool.Pool` + `pgdb.Queries`. Transactional `Add`. Error mapping (`pgx.ErrNoRows`, `23505`, etc.). |
| `internal/storage/postgres/migrate.go` | Thin wrapper around `golang-migrate/migrate/v4` so the api binary, the test suite, and the `make migrate-up` CLI all share one code path. |
| `compose.yaml` | Single `db` service (postgres:16-alpine) with named volume + healthcheck. Reads credentials from `.env`. |
| `.env.example` | Template of required env vars. Copy to `.env` (gitignored). |
| `Makefile` | `install-tools`, `sqlc-gen`, `migrate-up`, `migrate-down`, `compose-up`, `compose-down`, `psql`, `test`, `test-race`. |

## Quick start

```bash
# 1. One-time toolchain.
make install-tools                  # installs sqlc + golang-migrate at pinned versions
cp .env.example .env                # populates DATABASE_URL etc.

# 2. Bring up Postgres.
make compose-up

# 3. Apply migrations.
make migrate-up                     # uses DATABASE_URL from your shell or .env

# 4. Run the api against postgres.
go run ./cmd/api -storage postgres
# or, with auto-migrate at boot:
go run ./cmd/api -storage postgres -migrate=true

# 5. Run the ingest CLI against postgres.
go run ./cmd/ingest -storage postgres -register \
  -name Alice -email alice@example.com -password secret123
go run ./cmd/ingest -storage postgres -list

# 6. Tear down (data persists in named volume `pgdata`).
make compose-down
```

## Environment variables

| Var | Read by | Default |
|---|---|---|
| `DATABASE_URL` | `cmd/api`, `cmd/ingest`, `make migrate-*` | none — `-db-dsn` flag overrides |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` / `POSTGRES_PORT` | `compose.yaml` | `app` / `app` / `app` / `5432` |
| `INGEST_PASSWORD` | `cmd/ingest` (auth fallback) | none — `-password` flag overrides |
| `TOKENS_CAPACITY` / `TOKENS_RATE_PER_MIN` | `cmd/api` | overrides `config/tokens.yaml` |

No credentials are checked into git. `compose.yaml` defaults are
`app/app/app` purely for local dev — production deployments override
via `.env` or process env.

## How transactional user creation works

Every `Repository.Add(ctx, u)` opens a real `pgx.Tx`, runs

```sql
INSERT INTO users …
INSERT INTO registration_log …
```

and commits. If either statement fails (most commonly the unique
index on `lower(email)` raising `SQLSTATE 23505`), `tx.Rollback`
runs and **neither** row lands. The integration test
`TestAdd_DuplicateEmailRollsBackBoth` verifies the audit row is
absent after a rejected duplicate.

## Error mapping

| Postgres / pgx error | Mapped to | HTTP / gRPC status |
|---|---|---|
| `pgx.ErrNoRows` | `model.ErrUserNotFound` | 404 / `NotFound` |
| `SQLSTATE 23505` (unique_violation) | `model.ErrDuplicateUser` | 409 / `AlreadyExists` |
| `SQLSTATE 23503` (foreign_key_violation) | `model.ErrInvalidUser` | 400 / `InvalidArgument` |
| anything else | `model.NewStorageError(err)` | 500 / `Internal` (real error logged) |

The transport handlers (`internal/transport/httpapi/errors.go`,
`internal/transport/grpc/server.go statusFor`) already know these
codes — no transport-layer changes were needed for Day 5.

## Regenerating sqlc

```bash
# After editing db/queries/users.sql or db/migrations/*.up.sql:
make sqlc-gen
go vet ./... && go build ./...
```

The generated files in `internal/storage/postgres/pgdb/` are committed
so reviewers don't have to install sqlc just to read a diff.

## Testing

```bash
make test-race             # unit + integration; spins fresh Postgres containers per test
make test SHORT=1          # set the -short flag and skip the testcontainers-go tests
go test -race -short ./... # equivalent
```

Each integration test in `user_repo_test.go` calls `tcpostgres.Run`
to start a fresh postgres:16-alpine container, runs `postgres.Migrate`
against it, exercises the repo, then `Terminate`s. Whole suite ≈ 10 s
on a warm Docker.
