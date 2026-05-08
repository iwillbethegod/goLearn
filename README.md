# goLearn — 5-day Go bootcamp project

A user-management service that grew across five days from a Day-1 CLI
into a Day-4 service mesh, and on Day 5 swapped its in-memory store
for **PostgreSQL** behind the same `user.Repository` interface. A
user registered through one entrypoint becomes the auth subject for
every other workflow: REST CRUD, concurrent CSV ingestion, and a
gRPC token-bucket coordinator — all backed by a transactional
Postgres-persisted store.

## Cohesive flow

```
   Day 1 register a user                 Day 3 manage via REST
   ──────────────────────                ──────────────────────
   $ ingest -register …                  $ curl POST /users
                                         $ curl GET /users
                  │
                  ▼ persisted in .data/users.json
                  │
   ┌──────────────┴──────────────┐
   │                             │
   Day 4 gRPC user service       Day 2 concurrent ingest
   ─────────────────────────     ────────────────────────
   $ grpc-demo -user $ID         $ ingest data/  ← takes user as auth
   GetTokens / TakeTokens                ↓
   ReturnTokens                  Day 4 cohesion glue
                                 ─────────────────────
                                 each file pre-counts rows,
                                 calls TakeTokens via gRPC,
                                 ReturnTokens(remaining) on
                                 partial failure or cancel.
```

The token bucket is the literal wire that ties the days together:
**Day 4** rate-limits **Day 2** ingestion against a **Day 1** user
identity, all through a contract first established by **Day 3**.

## Repo layout

```
api/openapi.yaml                  Day-3 contract
api/postman_collection.json       exported from the spec
proto/user/v1/user.proto          Day-4 contract
proto/gen/userpb/                 generated stubs (committed)
config/tokens.yaml                token-bucket defaults
docs/grpc-vs-rest.md              short comparison note
tools/tools.go                    pinned codegen tools
cmd/
  ingest/        Day-1 CLI (-register/-list/-delete-profile)
                 + Day-2 concurrent worker pool
                 + Day-4 gRPC token client (-grpc-addr)
  api/           Day-3 REST :8080 + Day-4 gRPC :9090
  grpc-demo/     Day-4 standalone gRPC client
  gen/           CSV fixture generator (Day-2 demos)
internal/
  model/         data structs (User, AppError, Record, FileStats)
  user/          Service + Repository contract + DedupStore
  storage/       jsonfile (default) + memory (tests)
  app/           strategy-pattern factory
  processor/     Processor strategy + CSVProcessor
  pool/          dynamically-resizable worker pool
  handler/       middleware chain (CancelCheck, Dedup, Process, …)
  ingest/        per-file driver + FileGate
  repl/          stdin command interface (add/remove/cancel/…)
  tokens/        token bucket + per-user store
  transport/
    httpapi/     Day-3 REST handler + generated router
    grpc/        Day-4 UserServiceServer impl
pkg/metrics/     atomic UsersAdded counter
```

Module path: `github.com/ashishsinghbhadoria/goLearn`. Go 1.22.
Direct deps: `golang.org/x/crypto/bcrypt`, `google.golang.org/grpc`,
`google.golang.org/protobuf`, `gopkg.in/yaml.v3`,
`github.com/getkin/kin-openapi`, `github.com/oapi-codegen/*`.

## Quick start (full end-to-end)

```bash
# 1. One-time toolchain (Go plugins are pinned in tools/tools.go;
#    protoc itself is a C++ binary, install via Homebrew).
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# 2. Build all binaries.
go build -o /tmp/golearn-api        ./cmd/api
go build -o /tmp/golearn-ingest     ./cmd/ingest
go build -o /tmp/golearn-grpc-demo  ./cmd/grpc-demo
go build -o /tmp/golearn-gen        ./cmd/gen

# 3. Day 1 — register a user (CLI, no server needed).
rm -rf .data
/tmp/golearn-ingest -register \
  -name Alice -email alice@example.com -password secret123
/tmp/golearn-ingest -list
# expect: one row for Alice with a u-<24 hex chars> id

# 4. Day 3 + Day 4 — start the user service (REST + gRPC).
/tmp/golearn-api -addr :8080 -grpc-addr :9090 \
  -store-path .data/users.json -tokens-config config/tokens.yaml &
sleep 1

ID=$(curl -sS http://localhost:8080/users | jq -r .[0].id)

# 5. Day 4 — verify the gRPC channel.
/tmp/golearn-grpc-demo -addr :9090 -user "$ID"
# expect: available=20000 capacity=20000

# 6. Day 2 + Day 4 — concurrent CSV ingest with token gate.
/tmp/golearn-gen -files 4 -rows 250 -dup 15
/tmp/golearn-ingest -workers 8 -repl=false \
  -email alice@example.com -password secret123 \
  -grpc-addr :9090 \
  data/
# expect: 4 'tokens reserved' log lines, 1000 records processed,
#         ok=898 dedup=102 cancelled=0

# 7. Day 4 — confirm tokens were spent.
/tmp/golearn-grpc-demo -addr :9090 -user "$ID"
# expect: available≈19000 (plus a small refill)
```

Single end-to-end demo touches every day. The rest of this README
breaks down what each day delivered and where to find it.

---

## Day 1 — Go Fundamentals + Clean Architecture

> **Brief.** Topics: Go modules, variables & types · functions &
> multiple returns · error handling, structs & interfaces · handler
> → service → repository pattern · interfaces for abstraction ·
> manual dependency injection · custom error types · structured
> logging with slog. **Hands-on:** build a CLI-based User Manager,
> then immediately refactor it into a layered architecture, with
> `slog` wired across all layers from the start.

### Deliverables

- [x] **GitHub repo initialised, `go.mod` properly configured** —
      [go.mod](go.mod) (module `github.com/ashishsinghbhadoria/goLearn`).
- [x] **CLI program: Add user, List users, proper error handling** —
      [cmd/ingest/main.go](cmd/ingest/main.go) `-register` and `-list`.
      No `panic`s anywhere; all failures surface as `model.AppError`
      values that the CLI maps to a non-zero exit + readable stderr.
- [x] **Proper folder structure (handler → service → repository)** —
      `cmd/* (handler)` → [internal/user/service.go](internal/user/service.go) `(service)`
      → [internal/user/repository.go](internal/user/repository.go) `(repository iface)`
      → [internal/storage/](internal/storage/) `(impls)`.
- [x] **`UserRepository` interface with in-memory implementation** —
      [internal/user/repository.go](internal/user/repository.go) +
      [internal/storage/memory/user_repo.go](internal/storage/memory/user_repo.go).
      `internal/storage/jsonfile/` adds disk persistence as a second
      strategy; both swap via [internal/app/factory.go](internal/app/factory.go).
- [x] **Business logic separated from handlers** —
      [internal/user/service.go](internal/user/service.go) hosts every
      decision (`Register`, `Login`, `AddUser`, `UpdateUser`, …);
      handlers are pure transport translation.
- [x] **`slog` everywhere with consistent keys** — every `Service`
      method takes a `*slog.Logger`; key vocabulary is fixed at
      `user_id`, `email`, `error`. The logger is injected, never global.
- [x] **README with run instructions + architecture explanation** —
      this section + the architecture invariants at the bottom.

### Run

```bash
# Add a user (CLI):
go run ./cmd/ingest -register \
  -name Alice -email alice@example.com -password secret123

# List users (CLI):
go run ./cmd/ingest -list

# Self-delete (auth-required):
go run ./cmd/ingest -delete-profile \
  -email alice@example.com -password secret123
```

`-store-path` (default `.data/users.json`) and `-storage`
(`memory`|`jsonfile`, default `jsonfile`) pick the backend.

---

## Day 2 — Concurrency & Context

> **Brief.** Topics: goroutines · channels · `WaitGroup` · `Mutex`
> · context propagation · race detector. **Hands-on:** build a
> concurrent worker pool processing 1000 jobs across multiple CSVs,
> deduping records across files, supporting per-file cancellation
> mid-flight, with per-file/per-worker benchmark logs.

### Deliverables

- [x] **Worker pool implementation** —
      [internal/pool/pool.go](internal/pool/pool.go).
      Domain-agnostic: `Submit(ctx, fn)`. Workers add/remove at
      runtime; per-worker quit channels permit graceful drain.
- [x] **Context-based cancellation support** — three-tier hierarchy:
      `rootCtx` (`signal.NotifyContext` for SIGINT) → per-file ctx
      ([internal/ingest/runner.go](internal/ingest/runner.go) `runFile`)
      → per-job ctx propagated through the handler chain. Cancelling
      one file is independent of the others.
- [x] **Race-free execution (`go run -race`)** — `go test -race ./...`
      passes; the project's manual primitives are listed in
      [internal/handler/stats.go](internal/handler/stats.go) (atomics)
      and the per-bucket mutex in [internal/tokens/bucket.go](internal/tokens/bucket.go).
- [x] **Benchmark — time per file / per worker** —
      `cmd/ingest` summary lines (`file=… records=… handled=… duration=…`)
      plus the per-worker map in [internal/handler/stats.go](internal/handler/stats.go)
      (`Stats.PerWorker()` → `[]WorkerCount{ID, Count}`). 1M-row run
      over 8 workers shows distribution within a 0.18 % spread.
- [x] **Short markdown explaining concurrency approach** — this
      section + the per-record middleware pipeline laid out below.

### Concurrency approach

```
Runner.Run                             ← spawns 1 goroutine per file
   │
   ▼
runFile (per-file ctx)                 ← producer goroutine
   │
   ├─ FileGate.BeforeFile (Day-4)      ← reserves tokens
   │
   ├─ processor.Stream                 ← producer goroutine for CSV rows
   │     │
   │     ▼ (chan model.Record)
   │
   ├─ for rec := range stream:
   │     pool.Submit(ctx, closure)     ← bounded backpressure via buffered chan
   │
   ▼
pool.Pool.run                          ← N worker goroutines, mutex-guarded slice
   │
   ▼
handler.Chain                          ← middleware composition (per record)
   ├─ WithPerWorkerCount  (atomic)
   ├─ WithLogging         (slog, gated by -verbose)
   ├─ WithMetrics         (atomic)
   ├─ WithCancelCheck     (ctx.Err() short-circuits)
   ├─ WithDedup           (DedupStore.AddIfNew, mutex)
   └─ WithProcess         (mock 10–500ms, cancel-aware select)
```

`jobs.Wait()` per-file then `FileGate.AfterFile` settles the token
balance. Conservation invariant verified on every benchmark:
`ok + dedup + cancelled + parse_err == streamed records`.

### Run

```bash
go run ./cmd/gen -files 4 -rows 250 -dup 15        # 1000 records total
go run -race ./cmd/ingest -workers 8 -repl=false \
  -email alice@example.com -password secret123 \
  data/

# Per-file cancel demo (cancels users_b.csv ~50 ms in):
go run -race ./cmd/ingest -workers 8 -repl=false \
  -email alice@example.com -password secret123 \
  -cancel users_b.csv -cancel-after 50ms data/

# Live REPL (resize the pool while running): drop -repl=false.
# Commands: add N | remove N | status | files | cancel <name> | quit
```

---

## Day 3 — REST API (contract-first OpenAPI)

> **Brief.** Tools: Swagger / OpenAPI Generator (oapi-codegen).
> Topics: OpenAPI 3.0 spec · contract-first development · code
> generation · request validation. **Hands-on:** write `openapi.yaml`,
> generate Go server stubs, implement handlers.

### Deliverables

- [x] **Valid `openapi.yaml`** — [api/openapi.yaml](api/openapi.yaml).
      Five operations (list/create/get/update/delete) under `/users`
      with request validation, `Error` envelope, pagination params,
      `format: email`, hex `UserID` pattern.
- [x] **Generated server code committed** —
      [internal/transport/httpapi/gen/server.gen.go](internal/transport/httpapi/gen/server.gen.go)
      via [internal/transport/httpapi/gen/cfg.yaml](internal/transport/httpapi/gen/cfg.yaml).
      Tool pinned in [tools/tools.go](tools/tools.go) under
      `//go:build tools`.
- [x] **Implemented User CRUD endpoints** —
      [internal/transport/httpapi/handler.go](internal/transport/httpapi/handler.go)
      implements every method on the generated `ServerInterface`.
      `Handler` strips `password_hash` before encoding (regression
      test in [handler_test.go](internal/transport/httpapi/handler_test.go)).
- [x] **Validation working** — `withValidation` middleware in
      [cmd/api/middleware.go](cmd/api/middleware.go) wraps the
      `kin-openapi` validator and produces the spec's `Error` envelope.
- [x] **Postman collection exported** —
      [api/postman_collection.json](api/postman_collection.json),
      regenerated from the spec via `openapi-to-postmanv2`.
- [x] **README section explaining API-first approach** — this section
      + the workflow below.

### Workflow

```
api/openapi.yaml          ← hand-edited contract
        │
        ▼ go generate ./...   (//go:generate in gen/gen.go)
        │
proto/.../server.gen.go   ← regenerated: types + ServerInterface
        │
        ▼
httpapi.Handler           ← implements ServerInterface; compile errors
                            tell you exactly which methods are missing
                            after a spec change.
```

Per-request middleware chain in [cmd/api/middleware.go](cmd/api/middleware.go):
`recover → request-id → access-log → body-limit → spec-validator →
generated router → httpapi.Handler → user.Service → repository`.

### Run

```bash
# Start the user service (also starts gRPC :9090; pass -grpc-addr "" to disable).
go run ./cmd/api

curl -i -XPOST localhost:8080/users -H 'Content-Type: application/json' \
  -d '{"name":"Bob","email":"bob@example.com","password":"secret123"}'

curl -s localhost:8080/users | jq .
curl -i 'localhost:8080/users?limit=10&offset=0'
```

---

## Day 4 — gRPC + Protocol Buffers

> **Brief.** Tools: gRPC + Protocol Buffers (`protoc`). Topics:
> `.proto` structure · service definitions · unary RPC · code
> generation · gRPC client implementation. **Hands-on:** define
> `user.proto`, implement gRPC `UserService` (separate folder),
> write a small Go client that calls the gRPC server from the REST
> service.

### Deliverables

- [x] **`.proto` file committed** —
      [proto/user/v1/user.proto](proto/user/v1/user.proto). Five
      unary RPCs: `GetTokens` / `TakeTokens` / `ReturnTokens` for the
      rate-limit contract, plus `GetUser` / `ListUsers` mirroring the
      REST CRUD as demo material.
- [x] **Generated Go stubs** —
      [proto/gen/userpb/user.pb.go](proto/gen/userpb/user.pb.go) +
      [proto/gen/userpb/user_grpc.pb.go](proto/gen/userpb/user_grpc.pb.go).
      Plugins pinned in [tools/tools.go](tools/tools.go).
- [x] **Working gRPC server (separate port)** —
      [cmd/api/grpc.go](cmd/api/grpc.go) starts a listener on
      `:9090` (default) inside the same process as REST `:8080`.
      Implementation:
      [internal/transport/grpc/server.go](internal/transport/grpc/server.go).
      Token RPCs delegate to [internal/tokens/](internal/tokens/);
      CRUD RPCs delegate to `user.Service`. Domain errors map to
      `grpc/codes.Code` via `statusFor`.
- [x] **Client demonstrating RPC call** — two clients ship:
      - [cmd/grpc-demo/main.go](cmd/grpc-demo/main.go) — standalone
        learning-grade client (`-list`, `-get`, `-user`, `-take`,
        `-return`).
      - [cmd/ingest/tokens.go](cmd/ingest/tokens.go) — the **real**
        client; reads a CSV row count, calls `TakeTokens`, returns
        unused on partial failure. Implements `ingest.FileGate`.
- [x] **Short comparison note: REST vs gRPC** —
      [docs/grpc-vs-rest.md](docs/grpc-vs-rest.md).

### Glue: token bucket linking users to file processing

The token bucket is what fuses the days into a single coherent
project. Defaults (capacity 20 000, refill 10 000/min) come from
[config/tokens.yaml](config/tokens.yaml); env-vars
`TOKENS_CAPACITY` / `TOKENS_RATE_PER_MIN` override.

```
cmd/ingest                                         cmd/api
─────────                                          ─────────
runFile (per file):                                gRPC :9090
  rows  := countDataRows(path)
  TakeTokens(user_id, rows)   ──── unary RPC ────► tokens.Store.ForUser(id).Take(n)
  if !granted: skip file
  …process via worker pool…
  refund := rows − handled
  if refund > 0:
    ReturnTokens(user_id, refund) ─ unary RPC ───► tokens.Store.ForUser(id).Return(n)
```

Bucket is **lazy refill** (no goroutine, no ticker; arithmetic on
read). One mutex per bucket; per-user buckets in a `sync.Mutex`-guarded
map. `-race` clean (100 goroutines × 50 tokens vs capacity 1000 →
exactly 20 grants — no double-spending).

### Run

```bash
# Start the service (REST + gRPC).
go run ./cmd/api &
sleep 1

ID=$(curl -s localhost:8080/users | jq -r .[0].id)

# Standalone client.
go run ./cmd/grpc-demo -addr :9090 -user "$ID"
go run ./cmd/grpc-demo -addr :9090 -take -user "$ID" -count 100
go run ./cmd/grpc-demo -addr :9090 -return -user "$ID" -count 100

# Real client (the cohesion glue).
go run ./cmd/ingest -workers 8 -repl=false \
  -email alice@example.com -password secret123 \
  -grpc-addr :9090 \
  data_10/
```

---

## Day 5 — PostgreSQL Integration

> **Brief.** Topics: pgx/v5 + pgxpool · sqlc for type-safe SQL code
> generation · connection pooling · transactions · error mapping ·
> configuration management. **Hands-on:** replace the in-memory repo
> with PostgreSQL using pgxpool + sqlc.

### Deliverables

- [x] **DB schema migration file (golang-migrate)** —
      [db/migrations/000001_init.up.sql](db/migrations/000001_init.up.sql)
      + [`.down.sql`](db/migrations/000001_init.down.sql).
      Versioned `up`/`down` pair, applied via `make migrate-up` or
      via the in-process `postgres.Migrate(...)` helper.
- [x] **sqlc.yaml + generated query code committed** —
      [sqlc.yaml](sqlc.yaml) drives generation;
      [internal/storage/postgres/pgdb/](internal/storage/postgres/pgdb/)
      (`db.go`, `models.go`, `users.sql.go`) is the typed Go output.
      `make sqlc-gen` re-runs after editing
      [db/queries/users.sql](db/queries/users.sql).
- [x] **Unique email constraint** — `CREATE UNIQUE INDEX
      users_email_lower_idx ON users (lower(email))`. Closes the
      "Email keys not normalised" gap noted in the prior README's
      edge-case list — uppercase / lowercase / spaced variants of
      the same address all collide at the DB layer.
- [x] **Transactional user creation** — every `Repository.Add` opens
      a `pgx.Tx`, runs `INSERT users` + `INSERT registration_log`,
      and commits or rolls back atomically. Verified by
      `TestAdd_DuplicateEmailRollsBackBoth` —  a duplicate-email
      registration leaves zero rows in `registration_log` because
      the failed transaction took the audit row down with it.
- [x] **DB error properly mapped to API error** —
      [internal/storage/postgres/user_repo.go](internal/storage/postgres/user_repo.go)
      `mapErr`: `pgx.ErrNoRows` → `model.ErrUserNotFound`; SQLSTATE
      `23505` → `model.ErrDuplicateUser`; SQLSTATE `23503` →
      `model.ErrInvalidUser`; anything else → `model.NewStorageError`.
      The REST + gRPC error mappers already understood those codes,
      so the transport layer needed **zero changes**.
- [x] **Docker-ready DB config** — [compose.yaml](compose.yaml)
      brings up `postgres:16-alpine` with a healthcheck and a named
      volume. `make compose-up` / `make compose-down`.
- [x] **No hardcoded credentials** — DSN comes from `$DATABASE_URL`
      with a `-db-dsn` flag override. `compose.yaml` reads
      `${POSTGRES_USER}` / `${POSTGRES_PASSWORD}` from `.env`
      ([.env.example](.env.example) is the template; real `.env` is
      gitignored).

### How it fits

`internal/app/factory.go` gains a third case alongside `memory` and
`jsonfile`. Every layer above it — `user.Service`, the REST handler,
the gRPC handler, the day-2 ingest pipeline, the day-4 token gate,
and all four cohesion tests — works **unchanged** against the
postgres backend. The `cohesion_test.go` suite that exercises the
REST + gRPC contract from day 4 now also serves as a parity check:
behaviour identical regardless of which strategy is selected.

### Run

```bash
# Toolchain.
make install-tools                # sqlc + golang-migrate at pinned versions
cp .env.example .env

# Database up.
make compose-up && make migrate-up

# Day-1 CLI against postgres.
go run ./cmd/ingest -storage postgres -register \
  -name Alice -email alice@example.com -password secret123
go run ./cmd/ingest -storage postgres -list

# Day-3 + Day-4 service against postgres.
go run ./cmd/api -storage postgres -migrate=true

# Verify the unique-email rollback.
curl -sS -XPOST localhost:8080/users -H 'Content-Type: application/json' \
  -d '{"name":"A","email":"alice@example.com","password":"secret123"}'   # 201
curl -sS -XPOST localhost:8080/users -H 'Content-Type: application/json' \
  -d '{"name":"A2","email":"ALICE@EXAMPLE.COM","password":"secret123"}'  # 409 duplicate_user

# Inspect the audit table.
make psql -e "DATABASE_URL=postgres://app:app@localhost:5432/app?sslmode=disable"
# > SELECT user_id, event FROM registration_log;
```

### Tests

The integration suite under
[internal/storage/postgres/user_repo_test.go](internal/storage/postgres/user_repo_test.go)
uses `testcontainers-go` to spin a fresh disposable Postgres for each
test. Whole suite ≈ 10 s on a warm Docker. Skipped automatically with
`-short`. Eight cases cover transactional Add (atomic happy path +
rollback on duplicate), case-insensitive lookup, `ErrUserNotFound`
mapping, password preservation on partial Update, email-collision on
Update, FK cascade on Remove.

For an ops-flavoured walkthrough of the Postgres backend, see
[docs/postgres.md](docs/postgres.md).

---

## Day 6 — NATS + OpenTelemetry

Day 6 turns the user service into an event source and adds end-to-end
tracing across the whole stack.

```
POST /users → cmd/api → user.Service.Register
                          ├─ INSERT users (Postgres, otelpgx span)
                          └─ publish user.created (NATS JetStream)
                                       │
                                       ▼ traceparent header
                                cmd/consumer → INSERT notifications
```

- **Producer**: `internal/events/nats` ensures stream `USERS` (subjects
  `user.>`, retention=Limits, MaxAge=24 h, MaxBytes=1 GiB) and
  publishes `user.created.v1` events synchronously via JetStream.
  The publish runs on a *detached* ctx (`context.WithoutCancel` +
  carry SpanContext) so a client disconnect post-DB-commit doesn't
  drop the event.
- **Consumer**: `cmd/consumer` owns a durable pull consumer
  `user-welcome` with `AckExplicit` + `MaxDeliver=5`. Each event
  becomes a row in `notifications` via `INSERT … ON CONFLICT (event_id)
  DO NOTHING` — JetStream redelivery is a silent no-op insert.
- **Tracing**: `internal/observability` boots a `TracerProvider` that
  picks OTLP→Jaeger / stdout / no-op based on env. `otelhttp` wraps
  the mux, `otelgrpc` `StatsHandler` wraps the gRPC server, `otelpgx`
  wraps the pgxpool, and the NATS publisher injects W3C `traceparent`
  into headers. One `trace_id` spans REST → DB → NATS → consumer → DB.
- **Log correlation**: `pkg/logger.TraceHandler` is a `slog.Handler`
  decorator that pulls `trace_id` / `span_id` from ctx and adds them
  as attrs. Every hot-path log line uses `InfoContext(ctx, …)` so the
  handler sees the SpanContext.

### Demo

```bash
make compose-up        # db + nats + jaeger
make migrate-up        # migrations 000001 + 000002
make api-run &
make consumer-run &
curl -X POST localhost:8080/users \
  -H "content-type: application/json" \
  -d '{"name":"Ada","email":"ada@example.com","password":"hunter22"}'

# Then open http://localhost:16686 and search service goLearn-api.
```

For deeper notes:

- [docs/eventing.md](docs/eventing.md) — subjects, JetStream config,
  payload schema, idempotency, dual-write caveat + outbox future-work.
- [docs/observability.md](docs/observability.md) — OTel bootstrap,
  exporter modes, slog correlation, init-order rules, the
  `InfoContext` footgun, demo trace.

---

## Architecture invariants

1. **Contract → Service → Repository.** REST and gRPC handlers are
   thin translators. All decisions live in `user.Service` /
   `tokens.Store`. Storage is reachable only through the
   `user.Repository` interface.
2. **Repository is the storage seam.** Swapping `jsonfile` for
   `memory` (or a future Postgres backend) is a one-line change in
   [internal/app/factory.go](internal/app/factory.go); nothing else
   needs to know.
3. **`model.AppError` is the cross-cutting error vocabulary.** HTTP
   status mapping ([httpapi/errors.go](internal/transport/httpapi/errors.go)),
   gRPC code mapping ([grpc/server.go statusFor](internal/transport/grpc/server.go)),
   CLI exit messages, and structured-log fields all read from the same
   set of `model.Code…` enums.

## Edge cases & known limitations

Selected sharp edges (not blockers — noted for future hardening):

- **CSV header positional, not named.** Reordered columns swap fields
  silently. UTF-8 BOM is stripped; column-name validation is the next
  step.
- **Email normalisation in `jsonfile` / `memory` backends.**
  `Repository.Add` for the file/memory strategies stores the raw
  email as written. The Day-5 **postgres** backend enforces case-
  insensitive uniqueness at the DB layer (`lower(email)` unique
  index) so this gap only applies if you pick the legacy backends.
- **Cross-process consistency in `jsonfile` mode.** `cmd/api` loads
  `.data/users.json` once at boot. Writes from a concurrently-running
  `cmd/ingest -register` are invisible to the running api until
  restart. Mitigation: use the **postgres** backend for any
  multi-process workflow — both `cmd/api` and `cmd/ingest` see the
  same rows immediately.
- **Tokens are in-memory.** Bucket state is lost on api restart.
  Persistence is a Day-6+ concern (Day 5 chose to keep tokens
  decoupled from the user repo to keep the transactional demo
  focused on `users` + `registration_log`).
- **gRPC channel is plaintext.** `insecure.NewCredentials()` is fine
  for localhost; production needs TLS + interceptor-based auth.

## Generating / regenerating

```bash
# OpenAPI (Day 3) → Go.
go generate ./internal/transport/httpapi/gen/...

# Protobuf (Day 4) → Go.
go generate ./proto/...

# SQL queries (Day 5) → Go via sqlc.
make sqlc-gen

# Both (and anything else with //go:generate).
go generate ./...

# Tests.
go vet ./... && go build ./... && go test -race ./...
```
