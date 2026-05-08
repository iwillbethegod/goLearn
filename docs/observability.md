# Observability Design (Day 6)

Day 6 ships a unified trace pipeline: every REST request, gRPC RPC,
DB query, NATS publish, and consumer span lives under one trace_id,
exported via OTLP/gRPC to a local Jaeger container, and every `slog`
log line carries the same `trace_id` so logs and traces are
clickable-twins.

```
┌────────────────────────┐
│ POST /users            │  root span (otelhttp)
└─┬──────────────────────┘
  │
  ├─ user.Service.Register
  │  ├─ pgx INSERT users          (otelpgx)
  │  └─ nats publish user.created (manual span + W3C headers)
  │
  │  ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ traceparent header  ─ ─ ─ ─ ▶
  │
  └─ (request returns to client)

                                   ┌──────────────────────────┐
                                   │ consumer.user.created    │  child span
                                   └─┬────────────────────────┘
                                     │
                                     └─ pgx INSERT notifications  (otelpgx)
```

## Layers

| Layer        | Library                                  | Where                                  |
| ------------ | ---------------------------------------- | -------------------------------------- |
| HTTP         | `otelhttp.NewHandler`                    | `cmd/api/main.go` (outermost wrap)     |
| gRPC         | `otelgrpc.NewServerHandler` (StatsHandler) | `cmd/api/grpc.go`                    |
| Postgres     | `otelpgx.NewTracer`                      | `internal/storage/postgres/user_repo.go`, `cmd/consumer/main.go` |
| NATS publish | manual W3C inject + `nats.HeaderCarrier` | `internal/events/nats/publisher.go`    |
| NATS receive | manual W3C extract + `nats.HeaderCarrier` | `cmd/consumer/main.go`                |

## Bootstrap

`internal/observability/otel.go` owns the `TracerProvider`. One call
at process start, one deferred shutdown:

```go
otelShutdown, err := observability.Init(ctx, observability.Config{
    ServiceName: "goLearn-api",
    Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
    Exporter:    os.Getenv("OTEL_TRACES_EXPORTER"),
})
defer otelShutdown(...)
```

Exporter selection:

| Env                                           | Mode                                                  |
| --------------------------------------------- | ----------------------------------------------------- |
| `OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317`  | OTLP/gRPC to Jaeger (default in `make compose-up`)    |
| `OTEL_TRACES_EXPORTER=stdout`                 | JSON spans on stderr alongside slog                   |
| (both empty) or `OTEL_TRACES_EXPORTER=none`   | No-op TracerProvider — zero per-span overhead         |

The W3C `TraceContext` + `Baggage` propagators are set as global so
any `otel.GetTextMapPropagator().Inject/Extract` call uses the same
format on both ends of the NATS hop.

## slog ↔ trace correlation

`pkg/logger/trace_handler.go` decorates any inner `slog.Handler` with
a `Handle(ctx, r)` that pulls `trace.SpanContextFromContext(ctx)` and
adds `trace_id` + `span_id` attrs.

```go
base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
logger := slog.New(pkglogger.NewTraceHandler(base))
```

All four `slog.Handler` methods (`Enabled`, `Handle`, `WithAttrs`,
`WithGroup`) are forwarded so `logger.With(...)` chains and
`logger.WithGroup("http")` nesting keep working.

### Footgun: `logger.Info(...)` vs `logger.InfoContext(ctx, ...)`

`*slog.Logger.Info(...)` calls `Handle` with `ctx = context.Background()`.
The TraceHandler sees no SpanContext and emits no `trace_id`. To
correlate, hot-path log sites must use the ctx-aware variants:

```go
// ✗ Won't show trace_id even when called inside a span:
logger.Info("user registered", "user_id", u.ID)

// ✓ Carries SpanContext through to the handler:
logger.InfoContext(ctx, "user registered", "user_id", u.ID)
```

Day-6 already converted the hot paths in `internal/user/service.go`,
`internal/storage/postgres/user_repo.go`, `cmd/api/middleware.go`,
and `cmd/api/grpc.go`. New code that handles a request must follow
the convention.

## Init order matters

`otelpgx.NewTracer(otelpgx.WithTracerProvider(otel.GetTracerProvider()))`
reads the provider at construction time. So:

1. `observability.Init(ctx, cfg)` MUST run first.
2. THEN `postgres.NewUserRepo(...)` (or any pgxpool wired with otelpgx).
3. THEN `grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))`.
4. THEN HTTP handlers wrap.

The plan and code path enforce this in `cmd/api/main.go`'s `run()`.

## `os.Exit` traps

A direct `os.Exit(1)` does NOT run deferred functions, so a deferred
`tp.Shutdown(ctx)` never flushes buffered spans. Both binaries
(`cmd/api`, `cmd/ingest`, `cmd/consumer`) use a `run() error` shape:

```go
func main() {
    if err := run(); err != nil {
        fmt.Fprintln(os.Stderr, "fatal:", err)
        os.Exit(1)
    }
}
```

Every error path in `run()` is `return fmt.Errorf(...)`, so all
deferred shutdowns fire on the way out.

## Demo

```bash
make compose-up                 # db + nats + jaeger:16686
make migrate-up
make api-run &
make consumer-run &
curl -X POST localhost:8080/users \
    -H "content-type: application/json" \
    -d '{"name":"Ada","email":"ada@example.com","password":"hunter22"}'
```

Then open `http://localhost:16686`, search service `goLearn-api`, and
the trace shows:

```
POST /users
 ├─ user.Service.Register     (manual via stdlib, no extra wrap)
 ├─ pgx.exec INSERT users
 ├─ pgx.exec INSERT registration_log
 └─ nats publish user.created
        │
        └─ consumer.user.created    [linked: same trace_id]
              └─ pgx.exec INSERT notifications
```

`slog` output on both sides:

```
time=... level=INFO msg=http  request_id=ab12 trace_id=4f3a... span_id=...
time=... level=INFO msg="user.created processed"  trace_id=4f3a... event_id=5f3a...
```

Same `trace_id`. Same incident. Pivot from Jaeger to logs and back
without correlating timestamps.

## Test posture

- Tests run with `OTEL_TRACES_EXPORTER=` (unset) → no-op
  TracerProvider, zero per-span overhead, no exporter dial.
- `internal/storage/postgres/user_repo_test.go` has a `TestMain` that
  pins `otel.SetTracerProvider(tracenoop.NewTracerProvider())` to
  silence "no provider registered" warnings during testcontainer
  runs.
- `internal/events/nats/publisher_test.go` sets up a real
  TracerProvider so the W3C inject/extract round-trip can be
  asserted.
