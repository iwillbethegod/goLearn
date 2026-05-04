# goLearn — Day 1 + Day 2 + Day 3: User Service + Concurrent CSV Ingestion + REST API

Three layered exercises in one repo:

- **Day 1**: a user service with a Strategy-pattern repository
  (memory / jsonfile / postgres-stub), service layer, and gRPC-style
  handler. Persistence via a JSON file with bcrypt-hashed passwords.
- **Day 2**: a concurrent CSV ingestion pipeline built around a
  Processor strategy interface and a middleware-based handler chain.
  Multiple files run in parallel, the pool resizes at runtime via an
  interactive REPL, duplicates are deduped across files, individual
  files can be cancelled mid-flight, and the whole thing is
  race-detector clean.
- **Day 3**: a contract-first REST API on top of the day-1 service.
  `api/openapi.yaml` is the source of truth; `oapi-codegen` produces
  the server interface and types; a hand-written handler implements
  the interface against `user.Service`; request validation is
  enforced by middleware before handlers run.

The CSV ingester (`cmd/ingest`) is gated behind email + password
auth against the day-1 user store. The REST API (`cmd/api`) is
unauthenticated for now; the spec declares `BasicAuth` under
`securitySchemes` for a future flip-on. All three CLIs (`cmd/api`,
`cmd/ingest`, `cmd/server`) share the same persistent store, so a
user registered via one is visible to the others.

## Topics exercised

**Day 1**: Strategy pattern · interface segregation · service layer ·
file-backed persistence · `log/slog` · `sync.RWMutex` · bcrypt.

**Day 2**: Goroutines · channels · `sync.WaitGroup` · `sync.Mutex` ·
`sync/atomic` · `context` propagation · the race detector · middleware
chain · functional options.

**Day 3**: OpenAPI 3.0 · contract-first design · `oapi-codegen`
(std-http-server target) · request validation via the kin-openapi
middleware · `//go:generate` workflow · tool-dependency pinning via
build-tag-gated `tools/tools.go`.

## Layout

```
api/
  openapi.yaml             ← contract-first OpenAPI 3.0 spec
  postman_collection.json  ← exported Postman collection
tools/
  tools.go                 ← build-tag-pinned oapi-codegen tool
cmd/
  api/        HTTP API server entrypoint (Day 3)
  gen/        generates sample CSVs with cross-file duplicates
  ingest/     authenticated CSV ingestion CLI
              (register, login, ingest, delete-profile)
  server/     interactive user-management CLI
              (-add, -remove, -list)
internal/
  user/       User model, Repository interface, Service (Register,
              Login, AddUser, DeleteByEmail, RemoveUser, …),
              transient DedupStore for ingest-side dedup,
              email-validation regex, AppError taxonomy.
  storage/
    jsonfile/   persistent file-backed user.Repository (atomic
                rename-on-write, lower-cased email index).
    memory/     in-memory user.Repository (also satisfies the
                interface; used by tests and ad-hoc runs).
    postgres/   stub implementation returning ErrPostgresNotImplemented.
  app/        factory: NewRepository(cfg) — strategy switch over
              memory / jsonfile / postgres.
  processor/  Processor interface + Registry + CSVProcessor.
  pool/       Domain-agnostic worker pool: Submit(ctx, fn). Workers
              inject their ID via context value; nothing else.
  handler/    Per-record pipeline: Outcome, Handler, Middleware,
              Chain, Stats/Snapshot, and middleware constructors
              (CancelCheck, Dedup, Process, Metrics, Logging,
              PerWorkerCount).
  ingest/     File/folder discovery + per-file driver. Submits one
              closure per record into the pool; closures invoke the
              handler chain.
  repl/       Stdin command interface for the running ingest job
              (add/remove/status/files/cancel/quit) over small
              interfaces.
  transport/
    grpc/      toy in-process handler that wraps user.Service.
    httpapi/   HTTP handler implementing the generated ServerInterface,
               error mapper, JSON helpers; sub-package gen/ holds the
               oapi-codegen output (regenerated via go generate).
  external/maps/   placeholder external service.
pkg/
  logger/   slog default-handler factory.
  metrics/  trivial counter (UsersAdded).
```

Module path: `github.com/ashishsinghbhadoria/goLearn`. Go 1.22+
(some Day-3 transitive deps require 1.25+ at build time).

**Direct runtime dependencies**:
- `golang.org/x/crypto/bcrypt` — password hashing (Day 1).
- `github.com/oapi-codegen/runtime` — generated-code helpers (Day 3).
- `github.com/oapi-codegen/nethttp-middleware` — request validator (Day 3).
- `github.com/getkin/kin-openapi` — OpenAPI spec parser (Day 3).

**Build-only**: `github.com/oapi-codegen/oapi-codegen/v2` (pinned in
`tools/tools.go`, runs only during `go generate`).

### Dependency direction

```
              user
              ▲   ▲
              │   │
       processor  pool
              ▲   ▲ ▲
              │   │ │
              ingest ──► handler (Stats, chain) ──► pool (WorkerID)
                  │                ▲
                  │                │
                  └─► repl ────────┘
                  ▲
                  │
              cmd/ingest (main, mock, summary)
```

`pool` is the only package with no internal imports. `handler` knows
about `user` (for the User type in the Handler signature) and `pool`
(for `WorkerID`), but not vice versa.

---

## Architecture walkthrough

### The four binaries

| Binary | Purpose | How to run |
|---|---|---|
| **`cmd/api`** | HTTP REST API for User CRUD. Generated from `api/openapi.yaml` via `oapi-codegen`. | `go run ./cmd/api -addr :8080` |
| **`cmd/ingest`** | Concurrent CSV ingestion with auth-gated access. Day-2 + day-1 wired together. Also handles `-register` and `-delete-profile`. | `go run ./cmd/ingest -register …` then `go run ./cmd/ingest -email … data/` |
| **`cmd/server`** | Interactive user management CLI: `-add`, `-list`, `-remove`. Same store as `cmd/api` and `cmd/ingest`. | `go run ./cmd/server -add` |
| **`cmd/gen`** | Test-fixture generator: writes `users_a.csv` … with controllable cross-file duplicates. | `go run ./cmd/gen -files 4 -rows 250 -dup 15` |

All three user-facing binaries (`cmd/api`, `cmd/ingest`, `cmd/server`)
share the **same `app.NewRepository` factory** and persist to the
same `users.json`, so a record created via any one is immediately
visible to the others.

### Layered architecture

```
                  ┌──────────────────────────────────────────┐
  USER-FACING     │   cmd/api    cmd/ingest   cmd/server     │
                  └──────────────┬───────────┬───────────────┘
                                 ▼           ▼
                  ┌──────────────────────────────────────────┐
  TRANSPORT       │  httpapi.Handler   ingest.Runner   bufio │
                  │  (gen ServerIface)  (handler chain)      │
                  └──────────────┬───────────┬───────────────┘
                                 ▼           ▼
                  ┌──────────────────────────────────────────┐
  DOMAIN /        │   user.Service          user.DedupStore  │
  USE-CASE        │   (Register, Login,     (transient ingest│
                  │    UpdateUser, …)        dedup gate)     │
                  └──────────────┬───────────────────────────┘
                                 ▼
                  ┌──────────────────────────────────────────┐
  PERSISTENCE     │  user.Repository (interface)             │
                  │     ├── jsonfile (default, atomic write) │
                  │     ├── memory   (test backend)          │
                  │     └── postgres (stub, returns notImpl) │
                  └──────────────────────────────────────────┘
```

The transport layer never imports the storage package. Service and
DedupStore know about `User` (the value type) but not about how
records are persisted. The factory (`internal/app/factory.go`) wires
a `user.Repository` interface from the runtime config — this is the
strategy seam that lets us swap backends.

### HTTP API endpoints

Spec lives at [`api/openapi.yaml`](api/openapi.yaml); generated
server interface at [`internal/transport/httpapi/gen/server.gen.go`](internal/transport/httpapi/gen/server.gen.go);
hand-written impl at [`internal/transport/httpapi/handler.go`](internal/transport/httpapi/handler.go).

| Method | Path | Op | Body / params | Success | Possible failures |
|---|---|---|---|---|---|
| `GET` | `/users` | `listUsers` | `?limit=N&offset=M` (defaults 100, 0; max 1000) | `200 [User…]`, `X-Total-Count` header | `400` validation, `500` storage |
| `POST` | `/users` | `createUser` | `{name, email, password ≥ 8}` | `201 User`, `Location:/users/{id}` | `400` validation/decode, `409` duplicate, `500` |
| `GET` | `/users/{id}` | `getUser` | path: `id matches ^u-[0-9a-f]+$` | `200 User` | `400`, `404` not found, `500` |
| `PUT` | `/users/{id}` | `updateUser` | `{name?, email?}` | `200 User` | `400`, `404`, `409` email taken, `500` |
| `DELETE` | `/users/{id}` | `deleteUser` | — | `204` no content | `400`, `404`, `500` |

Every error response uses the same envelope:

```json
{ "code": "duplicate_user", "message": "user already exists" }
```

`User` responses **never** carry the `password_hash` field
(enforced both by the `User` schema in the spec and by manual field
copying in `httpapi.toAPIUser`). Tested via
`TestPasswordHashNeverInResponse`.

### Per-request middleware chain (cmd/api)

```
client request
   │
   ▼
withRecover            ← any panic below → 500 + error envelope, stack to log
   │
   ▼
withRequestID          ← 8-byte hex; round-tripped on X-Request-Id; logged
   │
   ▼
withAccessLog          ← single info-line per request (method/path/status/dur)
   │
   ▼
withBodyLimit          ← cap r.Body at 1 MiB via http.MaxBytesReader
   │
   ▼
withValidation         ← kin-openapi validates body, query, path against spec
   │
   ▼
generated router (gen.HandlerWithOptions)
   │
   ▼
httpapi.Handler        ← decode → svc.X → toAPIUser → writeJSON
   │
   ▼
user.Service           ← business rules + bcrypt + ID generation
   │
   ▼
user.Repository        ← jsonfile or memory or postgres-stub
```

### CSV ingest pipeline (cmd/ingest, no auth shown)

```
runner.Runner
   ├── for each path: spawn goroutine, derive per-file ctx
   │      │
   │      ▼
   │   processor.CSVProcessor.Stream(ctx, path)
   │      ├── strip UTF-8 BOM, validate header columns
   │      └── read row → Record → channel
   │
   │      ▼ (range over stream)
   │   submit closure to pool
   │      │
   │      ▼
   └── pool.Pool ───► worker ───► handler.Chain:
                                   ├── WithPerWorkerCount   (counts every job)
                                   ├── WithLogging          (gated by -verbose)
                                   ├── WithMetrics          (tally outcomes)
                                   ├── WithCancelCheck      (short-circuit)
                                   ├── WithDedup            (DedupStore.AddIfNew)
                                   └── WithProcess          (mock 10–500ms work)
```

Cancellation cascades: SIGINT → root ctx cancel → per-file ctx cancel
→ csv goroutine sees ctx.Done() and stops sending → workers see
`ctx.Done()` mid-sleep and exit early → totals balance.

### Internal packages — purpose + key types

| Package | Purpose | Key exports |
|---|---|---|
| `internal/user` | Domain types + Service + DedupStore | `User`, `Repository`, `Service`, `DedupStore`, `AppError`, `IsValidEmail`, `MinPasswordLen` |
| `internal/storage/jsonfile` | Persistent file-backed `Repository`. Atomic rename on write; lower-cased email index. | `NewUserRepo(path, logger)` |
| `internal/storage/memory` | In-memory `Repository` (used by tests + ad-hoc runs). | `NewUserRepo(logger)` |
| `internal/storage/postgres` | Stub returning `ErrPostgresNotImplemented`. Sketch for a future backend. | `NewUserRepo(db)` |
| `internal/app` | `RepositoryConfig` factory for selecting a strategy at runtime. | `RepositoryConfig`, `NewRepository` |
| `internal/transport/httpapi` | HTTP handler implementing `gen.ServerInterface`; AppError → status mapper; JSON helpers. | `NewHandler` |
| `internal/transport/httpapi/gen` | `oapi-codegen` output (committed). | `ServerInterface`, `User`, `Error`, `GetSwagger` |
| `internal/transport/grpc` | Day-1 toy "gRPC-style" function wrapper. Not real gRPC. Kept for historical context. | `UserHandler.AddUser`, `UserHandler.ListUsers` |
| `internal/processor` | Strategy interface for record streams + CSV impl. | `Processor`, `Registry`, `CSVProcessor`, `Record` |
| `internal/pool` | Domain-agnostic worker pool with runtime resize. | `Pool`, `Job`, `WithQueueSize`, `WorkerID(ctx)` |
| `internal/handler` | Per-record middleware pipeline + Stats counters. | `Handler`, `Middleware`, `Chain`, `Stats`, `Snapshot`, `WithDedup`, `WithProcess`, `WithCancelCheck`, `WithMetrics`, `WithLogging`, `WithPerWorkerCount`, `Outcome` |
| `internal/repl` | Stdin command interface for the running ingest job. | `Run`, `Controls` |
| `internal/ingest` | File/folder discovery + per-file driver. | `Expand`, `Runner` |
| `pkg/logger` | `slog.Logger` factory (text handler, stdout). | `NewLogger` |
| `pkg/metrics` | Atomic counter for users-added (room to grow). | `Metrics`, `New`, `IncUserAdded`, `UsersAdded` |

### Source-of-truth invariants

- **`api/openapi.yaml` is the contract.** Anything in `gen/` is
  derived. Edit the spec → `go generate ./...` → fix any compile
  errors the new `ServerInterface` surfaces.
- **`user.Repository` is the persistence boundary.** No domain code
  bypasses it; backends are swapped at the factory.
- **`user.AppError` codes are the cross-cutting error vocabulary.**
  HTTP status mapping (`httpapi/errors.go`), CLI exit messages
  (`cmd/ingest`, `cmd/server`), and log fields all read from the
  same `Code` enum.

---

## Quick start

```bash
# 1. Build the race-instrumented binary once.
go build -race -o /tmp/ingest_race ./cmd/ingest

# 2. Generate fixtures (4 files × 250 rows = 1000, ~15% dups).
go run ./cmd/gen -files 4 -rows 250 -dup 15

# 3. Register a user. Persisted to .data/users.json by default.
/tmp/ingest_race -register \
  -email alice@example.com -name Alice -password secret123

# 4. Run an ingest as that user (login is required).
/tmp/ingest_race -workers 8 -queue 64 \
  -email alice@example.com -password secret123 \
  data/

# 5. Per-file cancellation demo.
/tmp/ingest_race -workers 8 -repl=false \
  -email alice@example.com -password secret123 \
  -cancel users_b.csv -cancel-after 50ms data/

# 6. High-throughput run with sub-millisecond mock work.
/tmp/ingest_race -workers 8 -queue 1024 -repl=false \
  -email alice@example.com -password secret123 \
  -work-min 10us -work-max 100us data_1m/

# 7. Pass the password via env var instead of -password.
INGEST_PASSWORD=secret123 /tmp/ingest_race \
  -workers 4 -repl=false -email alice@example.com data_10/

# 8. Delete the user's own profile.
/tmp/ingest_race -delete-profile \
  -email alice@example.com -password secret123
```

You can pass any mix of files and folders. Folders are scanned
non-recursively for files matching the active processor's extension.

The legacy interactive CLI is still available:

```bash
go run ./cmd/server -add        # prompts for name, email, password
go run ./cmd/server -list       # list all users
go run ./cmd/server -remove     # interactive removal by ID (no auth)
```

`cmd/server -add` calls the same `Service.Register` so users created
this way can log in via `cmd/ingest`.

---

## CLI reference

```
ingest [auth flags] [-register | -delete-profile | <path> [<path> ...]]
```

The CLI runs in one of three modes:

1. **Register** — `-register -email -name -password`. Creates a user
   in the persistent store and exits.
2. **Delete profile** — `-delete-profile -email -password`. Verifies
   the password, removes the user, exits.
3. **Ingest** (default) — `-email -password <path> ...`. Logs in,
   then runs the concurrent CSV pipeline.

### Auth flags

| Flag | Default | Meaning |
|---|---|---|
| `-register` | `false` | Register a new user and exit. Requires `-email -name -password`. |
| `-delete-profile` | `false` | Authenticate and delete the user's profile, then exit. |
| `-email` | `""` | User email (login or register). |
| `-password` | `""` | User password. Falls back to `$INGEST_PASSWORD` if empty. Min 8 chars on register. |
| `-name` | `""` | User name (register only). |
| `-store-path` | `.data/users.json` | Path to the persistent user store. |
| `-storage` | `jsonfile` | Storage strategy (`memory` or `jsonfile`). |

### Ingest flags (require login)

| Flag | Default | Meaning |
|---|---|---|
| `-workers` | `8` | Initial worker count. Resize at runtime via REPL. Must be ≥ 1. |
| `-queue` | `64` | Buffered job channel capacity (backpressure). Must be ≥ 0. |
| `-format` | `csv` | Processor name (must be registered). |
| `-repl` | `true` | Interactive REPL on stdin. Set `false` for batch runs. |
| `-cancel` | `""` | Comma-separated file basenames to auto-cancel mid-flight (demo). |
| `-cancel-after` | `30ms` | Delay before auto-cancellation fires. |
| `-work-min` | `10ms` | Minimum mock-work duration per record. |
| `-work-max` | `500ms` | Maximum mock-work duration per record. |
| `-verbose` | `false` | Emit a structured log line per record. High overhead at >100k rec/s. |

`<path>` may be a file or a directory. Mixed inputs work
(`./users_a.csv data_10k/`).

---

## Day 3 — REST API (contract-first with OpenAPI + oapi-codegen)

### Workflow

```
api/openapi.yaml                       ← you edit this (the contract)
       │
       ▼ go generate ./...             ← oapi-codegen reads the spec
internal/transport/httpapi/gen/        ← deterministic, checked in
  └── server.gen.go                    ← ServerInterface, types, swagger
       │
       ▼ implements
internal/transport/httpapi/handler.go  ← hand-written, calls user.Service
       │
       ▼ wired by
cmd/api/main.go                        ← validator middleware + std mux
```

The OpenAPI spec is the single source of truth. Changing an endpoint
or schema is a four-step loop: edit `api/openapi.yaml`, run
`go generate ./...`, fix any new compile errors in `handler.go`
(the generated `ServerInterface` will demand the new method or new
field), commit.

### Run the API server

```bash
# 1. (optional) regenerate from the spec — required if you edited the spec.
go generate ./...

# 2. Build and run.
go build -o /tmp/golearn-api ./cmd/api
/tmp/golearn-api -addr :8080 -store-path .data/users.json
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `:8080` | TCP listen address. |
| `-store-path` | `.data/users.json` | Persistent user store file. |
| `-storage` | `jsonfile` | `memory` or `jsonfile`. |
| `-shutdown-timeout` | `5s` | Graceful shutdown grace period. |

### Endpoints

| Method | Path | Operation | Body | Status codes |
|---|---|---|---|---|
| `GET` | `/users` | listUsers | — | `200`, `500` |
| `POST` | `/users` | createUser | `CreateUserRequest` | `201`, `400`, `409`, `500` |
| `GET` | `/users/{id}` | getUser | — | `200`, `400`, `404`, `500` |
| `PUT` | `/users/{id}` | updateUser | `UpdateUserRequest` | `200`, `400`, `404`, `409`, `500` |
| `DELETE` | `/users/{id}` | deleteUser | — | `204`, `400`, `404`, `500` |

`CreateUserRequest = {name, email, password}` (password ≥ 8 chars).
`UpdateUserRequest = {name?, email?}` (password change is **not** in scope for Day 3).
`User` responses never carry the `password_hash` field.

Errors share a single envelope: `{"code": "...", "message": "..."}`.

| AppError code | HTTP |
|---|---|
| `duplicate_user` | 409 |
| `invalid_user` / `invalid_email` / `invalid_password` | 400 |
| `user_not_found` | 404 |
| `invalid_credential` | 401 (reserved for a future auth flip-on) |
| `storage_error` | 500 |
| validator middleware | 400 (`code: "validation_failed"`) |

### Smoke test the running API

```bash
# Create a user.
ID=$(curl -s -XPOST localhost:8080/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Alice","email":"alice@example.com","password":"secret123"}' \
  | jq -r .id)

# Read it back.
curl -s localhost:8080/users/$ID

# Update name.
curl -s -XPUT localhost:8080/users/$ID \
  -H 'Content-Type: application/json' \
  -d '{"name":"Alice Renamed"}'

# List.
curl -s localhost:8080/users

# Delete.
curl -i -XDELETE localhost:8080/users/$ID    # → 204

# Validation failures (caught by middleware before reaching the handler):
curl -s -XPOST localhost:8080/users -H 'Content-Type: application/json' \
  -d '{"name":"X","password":"secret123"}'        # missing email → 400
curl -s -XPOST localhost:8080/users -H 'Content-Type: application/json' \
  -d '{"name":"X","email":"x@example.com","password":"short"}'  # short pw → 400
curl -s localhost:8080/users/not-a-valid-id        # bad id pattern → 400
```

The API server reuses the **same** `app.NewRepository` factory as
`cmd/server` and `cmd/ingest`, so a user created via `POST /users` is
immediately visible via `cmd/server -list` and can log in to
`cmd/ingest`.

### Two layers of validation

1. **Spec-level** — `nethttpmiddleware.OapiRequestValidator(swagger)`
   validates every request body, query param, and path param against
   the embedded spec **before** the handler runs. Rejected requests
   return 400 with `code: "validation_failed"`.
2. **Service-level** — `Service.Register` / `UpdateUser` still
   validate (password length, email regex, etc.) so the same rules
   apply to non-HTTP transports (`cmd/ingest`, `cmd/server`).

### Postman collection

`api/postman_collection.json` is exported from the spec so you can
import directly:

```bash
# Postman → File → Import → api/postman_collection.json
# Or regenerate from the latest spec:
npx -y openapi-to-postmanv2 -s api/openapi.yaml -o api/postman_collection.json -p
```

Postman also imports `api/openapi.yaml` directly via File → Import.

### Regenerate after spec changes

```bash
go generate ./...   # invokes oapi-codegen via tools/tools.go
go test ./...
```

The `//go:generate` directive lives at
`internal/transport/httpapi/gen/gen.go`. Tool dependency is pinned in
`tools/tools.go` under the `tools` build tag so the codegen tool
never ends up in any production binary.

---

## Interactive REPL

While ingestion runs, type commands on stdin:

```
repl: add [N] | remove [N] | status | files | cancel <name> | quit
```

| Command | Effect |
|---|---|
| `add [N]` | Spawn N more workers (default 1). New IDs are monotonically increasing. |
| `remove [N]` | Pop the N most-recently-added workers (LIFO). Each finishes its current job before exiting. |
| `status` | Print `workers`, `queued`, `ok`, `dedup`, `cancelled`, `parse_err`, `stored` and the per-worker counter map. |
| `files` | List currently-active files (basenames). |
| `cancel <name>` | Cancel the per-file context. Other files unaffected. |
| `quit` / `exit` | Cancel rootCtx (everything shuts down cleanly). |

```
> status
workers=8 queued=12 ok=237 dedup=11 cancelled=0 parse_err=0 stored=248
  w1=31 w2=33 w3=29 w4=32 w5=30 w6=29 w7=30 w8=33
> add 4
+ worker 9
+ worker 10
+ worker 11
+ worker 12
> cancel users_b.csv
cancelled file=users_b.csv
> remove 6
- worker 12
- worker 11
- worker 10
- worker 9
- worker 8
- worker 7
> quit
```

---

## Design

### Authentication & user store

`internal/user/Service` is the single entrypoint for credential
operations. It composes a `Repository` (storage) with bcrypt hashing
and email validation:

```go
func (s *Service) Register(ctx, name, email, password string) (User, error)
func (s *Service) Login(ctx, email, password string) (User, error)
func (s *Service) DeleteByEmail(ctx, email, password string) error
```

`Login` and `DeleteByEmail` both return `ErrInvalidCredential` for
*any* auth failure (wrong email, no such user, wrong password) so the
CLI cannot leak which one is incorrect.

Storage is selected by the `app.NewRepository` factory:

| Strategy | Use |
|---|---|
| `jsonfile` (default) | atomic-rename writes to `.data/users.json`. |
| `memory` | in-process map; lost on exit. Used by tests. |
| `postgres` | stub returning `ErrPostgresNotImplemented`. |

Passwords are bcrypt-hashed at the `DefaultCost` (10). Empty
`PasswordHash` means the record predates auth — those users cannot
log in, but the CLI lists them and removes them (compat with the
day-1 `cmd/server -add` flow before the password field existed).

The `Repository` interface satisfies Go's idiomatic
"accept interfaces, return concrete" — `Service` only sees the
interface; concrete repos are wired by `app.NewRepository`.



Every input format is a `Processor`:

```go
type Processor interface {
    Name() string                                                        // "csv"
    Extensions() []string                                                // [".csv"]
    Stream(ctx context.Context, path string) (<-chan Record, error)
}
```

[internal/processor/csv.go](internal/processor/csv.go) is the CSV
implementation. Adding JSON later is one line in
[cmd/ingest/main.go](cmd/ingest/main.go):
`reg.Register(JSONProcessor{})`. Pool, runner, REPL, and handler
chain all stay unchanged.

### Domain-agnostic pool

```go
// internal/pool/pool.go
type Job func(ctx context.Context)

func (p *Pool) Submit(ctx context.Context, fn Job) error
```

The pool has *no* knowledge of records, dedup, or business logic. A
worker accepts the caller's function and invokes it with a context
decorated with the worker's ID (read via `pool.WorkerID(ctx)`). The
runner builds a closure per record that captures the file, user, and
handler chain, then submits the closure.

This makes the pool reusable for any "run a function on a worker"
job and lets us test the pipeline middleware without touching the
pool at all.

### Middleware-based handler chain

The per-record pipeline is composed from independent middleware:

```go
chain := handler.Chain(handler.Terminal,
    handler.WithPerWorkerCount(stats),  // outermost: count every job
    handler.WithLogging(logger, verbose),
    handler.WithMetrics(stats),         // record final outcome
    handler.WithCancelCheck(),          // ctx done? short-circuit
    handler.WithDedup(store),           // dup? short-circuit
    handler.WithProcess(processFn),     // do the work
)
```

Each layer is single-responsibility and individually testable. Adding
retry, throttle, distributed tracing, etc. is a new middleware — no
existing code changes.

`Outcome` is one of `OutcomeOK`, `OutcomeDedup`, `OutcomeCancelled`,
or `OutcomeError`, and propagates back through the chain so outer
middleware (Metrics, Logging) sees what happened.

### Dynamic worker pool

Each worker holds its own `quit chan struct{}`. Resize ops are O(1):

```go
func (p *Pool) AddWorker() int                  // append + spawn goroutine
func (p *Pool) RemoveWorker() (int, error)      // pop + close(quit)
```

The worker loop selects across:

```go
case <-w.quit:           // RemoveWorker — graceful exit
case <-p.stop:           // Pool.Stop()  — shutdown
case <-rootCtx.Done():   // SIGINT / quit — cancel everything
case qj := <-p.jobs:     // run qj.fn(withWorkerID(qj.ctx, w.id))
```

`Stop()` drains any residual queued jobs by invoking each with a
cancelled context, so any per-job WaitGroup release in the closure's
`defer` fires.

### Cancellation hierarchy

```
rootCtx (signal.NotifyContext: SIGINT/SIGTERM)
  ├── per-file ctx 1 ──► passed into Processor.Stream + every Job
  ├── per-file ctx 2
  └── per-file ctx N
```

The runner stores per-file `CancelFunc`s by basename so the REPL can
cancel a single file. `WithProcess` middleware runs `select` against
the timer and `ctx.Done()`, so an in-flight record bails immediately
on cancel rather than riding out the rest of its sleep.

### Race-free guarantees

| State                   | Guard                          |
|-------------------------|--------------------------------|
| `Store.users` map       | `sync.Mutex`                   |
| Pool worker slice       | `sync.Mutex`                   |
| Job dispatch            | `chan queuedJob` (buffered)    |
| Stop signalling         | `chan struct{}` (close once)   |
| Pipeline counters       | `sync/atomic.Uint64`           |
| Per-worker counts       | `sync.Map[int]*atomic.Uint64`  |
| Per-file cancel map     | `sync.Mutex`                   |
| Cancellation cascade    | `context.Context` parent/child |

Verified with `-race` across the full benchmark matrix below.

---

## Benchmarks

All runs use the **race-instrumented** binary. Race overhead is
significant at high op-rates — non-race throughput would be ~3–5×
higher for the 1 M case.

Mock work duration is scaled per size to keep wall time tractable:

| Size | Work per record | Why |
|---|---|---|
| 10 | 10–500 ms | Realistic, finishes in <1 s |
| 1 000 | 10–500 ms | Realistic, ~30 s |
| 10 000 | 1–10 ms | Default work would take ~5 min |
| 1 000 000 | 10–100 µs | Default work would take ~9 hours |

### Baseline throughput (8 workers, `-verbose=false`)

| Records | Wall | ok | dedup | Throughput | Per-worker spread |
|---|---|---|---|---|---|
| 10 | 363 ms | 10 | 0 | 28 rec/s | 1–2 |
| 1 000 | 28.0 s | 898 | 102 | 36 rec/s | 115–134 |
| 10 000 | 6.4 s | 8 882 | 1 118 | **1 570 rec/s** | 1 214–1 295 |
| 1 000 000 | 7.7 s | 887 465 | 112 535 | **130 000 rec/s** | 124 584–125 294 (0.18 % spread) |

Pulling per-record logging behind `-verbose` lifted the 1 M throughput
from ~106k rec/s to ~130k rec/s — the stdlib `log` mutex was the
main bottleneck.

### Add / remove worker mid-flight

| Records | Initial | Resize | Wall | Result |
|---|---|---|---|---|
| 1 000 | 8 | +4 @ 3 s → 12, −6 @ 8 s → 6 | 32.9 s | LIFO removal verified; full-run workers handled 130–146 each, late-add/early-removed handled 19–39 |
| 10 000 | 4 | +4 @ 1 s → 8, −4 @ 4 s → 4 | 9.7 s | w1–w4=1 900–1 940 (full run); w5–w8=560–593 (1–4 s window) |
| 1 000 000 | 4 | +4 @ 1 s → 8, −4 @ 4 s → 4 | 15.1 s | w1–w4=210k–212k; w5–w8=38.5k–38.7k |

`ok + dedup` matches the baseline exactly in every run — no records
dropped. Removed workers finish their in-flight job before exiting.

### Cancel one file mid-flight

| Records | Cancel @ | Cancelled file's records | Cancelled file's wall | Other files' wall |
|---|---|---|---|---|
| 1 000 | 5 s | 67 / 250 | 6.3 s | 22.6–23.7 s |
| 10 000 | 1 s | 429 / 2 500 | 1.13 s | 5.18–5.20 s |
| 1 000 000 | 1 s | 24 970 / 250 000 | 1.01 s | 7.59 s |

The targeted file's wall closes within ~10 ms of `cancel-after` —
the cancel-aware `select` in `makeMockProcessRow` makes in-flight
records bail immediately. Other files run to completion as if the
cancel never happened.

---

## Edge cases & known limitations

The current code prioritises clarity over hardening. Sharp edges in
roughly decreasing priority:

### Data correctness (silent corruption — fix first)

1. **Header column order is positional, not named.** A file with
   header `name,id,email` would silently swap fields. Mitigation:
   validate header tokens, or build a name→index map from the header.
2. **UTF-8 BOM is not stripped.** A file written with a BOM leaks it
   into `row[0]` of the first record.
3. **Email keys are not normalised.** `User@Example.com` and
   `user@example.com` (or trailing whitespace) hash to different keys
   — silent dedup miss. Mitigation: normalise in `AddIfNew`.
4. **Empty email** rows all dedup to the same `""` key.

### Concurrency / API misuse

5. **`-workers 0`** is rejected at flag parse — Submit would block
   forever otherwise.
6. **Negative `-queue`** is clamped to 0 in `pool.WithQueueSize`.
7. **`Pool.Stop` contract**: callers must ensure no goroutine is
   mid-Submit when Stop is called. Today's `runner.Run` returns
   before Stop, so this is safe.
8. **Stuck FIFO / slow NFS as input** — Go's `ctx.Done()` does not
   interrupt a blocked file-read syscall, so a stuck pipe leaks the
   producer goroutine. Mitigation: wrap the file with a context-aware
   reader that calls `f.Close()` on `ctx.Done()`.
9. **Duplicate basenames across folders** — `runner.cancels` keys by
   basename, so the second overwrites the first. Mitigation: key by
   full path internally, accept basename in REPL with first-match
   semantics.

### Scale / resource

10. **Per-record logging at high rates** — gated behind `-verbose`.
    Default off keeps throughput high; turn on for debugging.
11. **Store memory grows unbounded** — at 1 B unique records you'd
    need ~100 GB. Mitigation: pluggable backend (Redis / SQLite),
    Bloom filter for fast-path checks, or sharded in-memory.
12. **Single `Store.mu` mutex** becomes contended above ~1 M rec/s.
    Mitigation: shard the map (`hash(email) % N`).
13. **No recursive folder traversal.** Sub-folders silently ignored.
    Mitigation: `-recursive` flag using `filepath.WalkDir`.
14. **Massive single CSV row** can OOM `csv.Reader`. Mitigation:
    `io.LimitReader` cap.
15. **REPL `add 1000000`** has no upper bound. Mitigation: cap at
    e.g. 10× initial `*workers`.

---

## Extending

### Add a new format (e.g. JSON)

```go
// internal/processor/json.go
type JSONProcessor struct{}

func (JSONProcessor) Name() string         { return "json" }
func (JSONProcessor) Extensions() []string { return []string{".json", ".jsonl"} }
func (JSONProcessor) Stream(ctx context.Context, path string) (<-chan Record, error) {
    // parse JSON / JSONL, emit Record on `out`, respect ctx.
}

// cmd/ingest/main.go (one line)
reg.Register(processor.JSONProcessor{})
```

```bash
ingest -format json data_jsonl/
```

### Add a new pipeline middleware (e.g. retry, throttle, tracing)

```go
// internal/handler/retry.go
func WithRetry(n int, backoff time.Duration) Middleware {
    return func(next Handler) Handler {
        return func(ctx context.Context, file string, u user.User) Outcome {
            for i := 0; i < n; i++ {
                if out := next(ctx, file, u); out != OutcomeError {
                    return out
                }
                time.Sleep(backoff)
            }
            return OutcomeError
        }
    }
}

// cmd/ingest/main.go (insert into chain)
chain := handler.Chain(handler.Terminal,
    handler.WithPerWorkerCount(stats),
    handler.WithMetrics(stats),
    handler.WithRetry(3, 100*time.Millisecond),  // ← new
    handler.WithCancelCheck(),
    handler.WithDedup(store),
    handler.WithProcess(processFn),
)
```

No changes elsewhere. The pool, runner, and REPL are entirely
unaware of the new middleware.
