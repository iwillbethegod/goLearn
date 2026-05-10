# Architecture

## The 30-second view

```
                 ┌──────────────────────────────────────────────┐
                 │  docker compose -f compose.full.yaml up      │
                 └──────────────────────────────────────────────┘
                                       │
       ┌───────────────────────────────┼───────────────────────────────┐
       │                               │                               │
       ▼                               ▼                               ▼
  ┌─────────┐         ┌────────────────────────────┐           ┌─────────────┐
  │   db    │◀────────┤  cmd/api  (REST + gRPC)    │ ─publish─▶│    nats     │
  │postgres │  pgx/   │   :8080 + :9090            │ JetStream │  USERS      │
  │  :5432  │  sqlc   │  ┌──────────────────────┐  │           │  user.>     │
  └─────────┘         │  │  user.Service        │  │           └──────┬──────┘
       ▲              │  │   bcrypt + ID gen    │  │                  │
       │              │  └──────────────────────┘  │                  │ pull
       │              │  ┌──────────────────────┐  │                  ▼
       │              │  │  tokens.Store        │  │           ┌─────────────┐
       │              │  │  per-user buckets    │  │           │  cmd/       │
       │              │  └──────────────────────┘  │           │  consumer   │
       │              └────────────┬───────────────┘           │  durable    │
       │                           │ otelpgx, otelhttp,        │  pull       │
       │                           │ otelgrpc spans            └──────┬──────┘
       │                           ▼                                  │
       │                    ┌────────────┐                            │
       └────INSERT──────────│  jaeger    │◀───────traces──────────────┘
            notifications   │  :16686 UI │
                            │  OTLP :4317│
                            └────────────┘
```

## Why each piece exists (the 7-day arc)

| Day | Component(s)                           | Bound forward via                           |
| --- | -------------------------------------- | ------------------------------------------- |
| 1   | `user.Service`, `Repository` interface | Day-2/3/4/5/6 ALL depend on `user.Service`  |
| 2   | `internal/pool`, `internal/handler`    | `cmd/ingest` worker pool; chain reused as middleware pattern |
| 3   | OpenAPI spec + `internal/transport/httpapi` | REST handlers translate to/from `user.Service` |
| 4   | `proto/user/v1`, `internal/transport/grpc`, `internal/tokens` | gRPC adapts to the *same* Service; tokens become a per-user gate for ingest |
| 5   | `internal/storage/postgres` (pgxpool + sqlc + migrations) | New `Repository` impl, jsonfile and memory still work unchanged |
| 6   | `internal/events/nats`, `cmd/consumer`, `internal/observability`, `pkg/logger` | `Service.Register` publishes; consumer reacts; one trace_id end-to-end |
| 7   | tests, Dockerfiles, compose.full, CI/CD | Every prior layer is now covered, containerised, and gated in CI |

The story: a single domain model (the `User`) flows through five
transport surfaces (CLI, REST, gRPC, NATS, slog/OTel), three
persistence backends (memory, jsonfile, Postgres), and one observable
trace tree.

## Layered view

```
┌──────────────────────────────────────────────────────────────────────┐
│ Transport                                                             │
│   cmd/api (REST :8080 + gRPC :9090)                                   │
│   cmd/ingest (CLI subcommands + concurrent CSV pipeline)              │
│   cmd/consumer (JetStream durable pull consumer)                      │
│   cmd/grpc-demo (smoke client)                                        │
│   cmd/gen (CSV fixture generator)                                     │
└────────────────────────────┬─────────────────────────────────────────┘
                             │
┌────────────────────────────┼─────────────────────────────────────────┐
│ Service                    │                                          │
│   user.Service  ←─── repo, logger, metrics, publisher (functional opt)│
│     Register / Login / AddUser / GetUser / UpdateUser /               │
│     RemoveUser / ListUsers / DeleteByEmail                            │
│   tokens.Store  per-user token bucket (Day 4)                         │
└────────────────────────────┬─────────────────────────────────────────┘
                             │
┌────────────────────────────┼─────────────────────────────────────────┐
│ Domain (no I/O)             │                                         │
│   internal/model            │  User, AppError, Record, FileStats,     │
│                             │  validation (IsValidEmail), error codes │
└────────────────────────────┬─────────────────────────────────────────┘
                             │
┌────────────────────────────┼─────────────────────────────────────────┐
│ Side-effect boundaries      │                                         │
│   user.Repository  ─────────┴─→ memory | jsonfile | postgres          │
│   user.Publisher  ──────────────→ noop | NATS JetStream               │
│   internal/observability ───────→ noop | stdout | OTLP→Jaeger         │
└──────────────────────────────────────────────────────────────────────┘
```

`user.Service` knows about exactly two interfaces — `Repository` and
`Publisher`. Both have a no-op default so the Service compiles and
runs in tests with zero infrastructure.

## Request → trace lifecycle

What happens for a single `POST /users`:

1. `otelhttp.NewHandler` opens span `POST /users` (root, kind=server).
2. Middleware chain: `withRecover → withRequestID → withAccessLog →
   withBodyLimit → withValidation` — `request_id` lands in ctx.
3. Generated `gen.HandlerWithOptions` dispatches to
   `httpapi.Handler.CreateUser` → `user.Service.Register`.
4. `Service.Register`:
   - validates name/email/password
   - bcrypts the password (~60 ms — see `dummyHash` for the
     constant-time login compensation)
   - generates a random 24-hex `id`
   - calls `repo.Add(ctx, user)` → otelpgx opens
     `pgx.exec INSERT users` span (sub-span of the request)
   - on success calls `publisher.PublishUserCreated(ctx, user)`:
     - derives **detached** ctx (`WithoutCancel` + carry SpanContext +
       2 s timeout) — a client disconnect after commit doesn't drop
       the event
     - injects W3C `traceparent` into NATS headers
     - `js.PublishMsg` → server-side ack
5. Response 201 returns to client.
6. Consumer's iterator sees the message:
   - extracts `traceparent` → starts span `consumer.user.created` as
     child of the same trace
   - parses payload, calls `pgdb.InsertNotification` → otelpgx opens
     `pgx.exec INSERT notifications` span
   - `msg.Ack()` on success
7. Span batch processor flushes to Jaeger (`OTLPv1.43`). Both spans
   share `trace_id`.

## Strategy seams

Three places where you can swap a backend without touching the rest:

| Seam               | Today                              | Future option                |
| ------------------ | ---------------------------------- | ---------------------------- |
| `user.Repository`  | memory / jsonfile / postgres       | Spanner / DynamoDB / etc.    |
| `user.Publisher`   | noop / NATS JetStream              | Kafka / Cloud Pub/Sub        |
| Trace exporter     | noop / stdout / OTLP→Jaeger        | OTLP→Tempo / Honeycomb / DD  |

Selection is driven by env at startup; tests use the no-op variants by
default so unit suites need no infrastructure.

## Deployment topology (compose.full.yaml)

```
┌──────────────────────── golearn-net (docker bridge) ────────────────────────┐
│                                                                              │
│   ┌──────┐   ┌──────────┐   ┌────────┐   ┌──────────┐   ┌────────────────┐  │
│   │  db  │   │ migrate  │   │  nats  │   │  jaeger  │   │      api       │  │
│   │      │◀──│ (oneshot)│   │   js   │   │ otlp+UI  │◀──│ REST + gRPC    │  │
│   └───▲──┘   └──────────┘   └────▲───┘   └──────────┘   └────────────────┘  │
│       │                          │                              │            │
│       │                          │                              │ publish    │
│       │                          │                              │ user.created
│       │                          │                              ▼            │
│       │                          │                       ┌────────────────┐  │
│       │                          └───────────────────────│   consumer     │  │
│       └──────────────────────────INSERT notifications────│   (pull/durable)│  │
│                                                          └────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘

  Host bindings (127.0.0.1 only):
    :5432 → db        :8080  → api HTTP
    :4222 → nats       :9090  → api gRPC
    :8222 → nats mon   :16686 → jaeger UI
    :4317 → jaeger OTLP gRPC
```

## Test surfaces

| Layer                      | Tests live in                                        |
| -------------------------- | ---------------------------------------------------- |
| Domain model + validation  | `internal/model/model_test.go`                       |
| Service (every method)     | `internal/user/service_test.go`                      |
| Repositories               | `internal/storage/{memory,jsonfile,postgres}/*_test.go` |
| HTTP transport             | `internal/transport/httpapi/*_test.go`               |
| gRPC transport             | `internal/transport/grpc/*_test.go`                  |
| Token bucket + config      | `internal/tokens/*_test.go`                          |
| Worker pool                | `internal/pool/pool_test.go`                         |
| Handler chain middleware   | `internal/handler/handler_test.go`                   |
| CSV processor              | `internal/processor/csv_test.go`                     |
| Ingest discovery           | `internal/ingest/discover_test.go`                   |
| HTTP middleware            | `cmd/api/middleware_test.go`                         |
| gRPC interceptor           | `cmd/api/grpc_test.go`                               |
| OTel bootstrap             | `internal/observability/otel_test.go`                |
| slog ↔ trace handler       | `pkg/logger/trace_handler_test.go`                   |
| Metrics                    | `pkg/metrics/metrics_test.go`                        |
| NATS publisher             | `internal/events/nats/publisher_test.go`             |
| **End-to-end** (testcontainers + embedded JetStream) | `internal/integration/e2e_test.go` (build tag `integration`) |

CI runs the unit suite + the integration suite + the coverage gate at
70% on every PR. Lint runs as a separate job.
