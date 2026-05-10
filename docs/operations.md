# Operations Runbook (Day 7)

Quick-reference for running, observing, and shutting down the full
goLearn stack.

## Start / stop

```bash
# Full capstone stack (db + nats + jaeger + api + consumer + migrate-once)
make stack-up
make stack-logs        # tail every container
make stack-down        # graceful shutdown, volumes persist

# Deps only (host-run dev loop)
make compose-up
make api-run &
make consumer-run &
make compose-down

# DB-only ops
make migrate-up
make migrate-down      # revert one
make psql

# NATS CLI inside the named network
make nats-cli          # then: nats stream ls / consumer info USERS user-welcome
```

## Smoke verification

```bash
curl -X POST localhost:8080/users \
  -H "content-type: application/json" \
  -d '{"name":"Ada","email":"ada@example.com","password":"hunter22"}'

# Expect 201 + JSON body. The slog line on stderr should include
# trace_id=... span_id=...

# DB row
psql "$DATABASE_URL" -c 'SELECT id, email FROM users'

# Notification row (consumer wrote it)
psql "$DATABASE_URL" -c 'SELECT event_id, user_id, kind FROM notifications'

# Trace in Jaeger
open http://localhost:16686
# Service: goLearn-api → click any trace → expect spans:
#   POST /users → user.Service.Register → pgx INSERT users
#                                       → nats publish user.created
#                                       → consumer.user.created → pgx INSERT notifications
```

## Graceful shutdown — what `SIGTERM` does

`docker compose stop` sends `SIGTERM` to every container; Compose then
waits 30 s before escalating to `SIGKILL`. Each binary runs the same
choreography:

1. **`signal.NotifyContext`** flips the root ctx to cancelled.
2. **HTTP** — `srv.Shutdown(shutdownCtx)` stops accepting new
   connections, drains in-flight requests, returns when idle.
3. **gRPC** — `srv.GracefulStop()` lets active RPCs finish; falls back
   to `srv.Stop()` after the configured timeout (5 s).
4. **NATS** — `Drain()` flushes pending publishes and pending acks
   before closing the TCP connection. Pulled messages that haven't
   been Acked yet redeliver via `MaxDeliver`.
5. **Postgres pool** — `repo.Close()` (`pgxpool.Close`) closes idle
   conns immediately and waits for active queries (or the deferred
   timeout) before exiting.
6. **TracerProvider** — `tp.Shutdown(ctx)` flushes batched spans to
   Jaeger so the last few seconds of work appears in the UI.

The `run() error` refactor (Day 6 Phase 1.4) was added precisely so
**every** error path returns through these defers — `os.Exit(1)`
would skip them and drop spans / leak DB conns.

If `docker compose stop` times out, that's a real bug — instrument the
slow phase with a span and inspect Jaeger.

## Inspecting JetStream

```bash
make nats-cli          # opens nats-box on the network
nats stream info USERS
# Look for: Storage: file, Retention: limits, Subjects: user.>
nats consumer info USERS user-welcome
# Look for: Pending ACKs, Redelivered, Last delivered seq
nats stream view USERS
```

## Inspecting OTel traces

- **Jaeger UI**: http://localhost:16686
- **Service**: `goLearn-api` (POST /users root), `goLearn-consumer`
  (linked spans under same trace_id).
- **Tag filter**: `error=true` to find failed RPCs.
- **Empty UI?** Check `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Set
  `OTEL_TRACES_EXPORTER=stdout` for a quick sanity check that the
  process emits *anything*.

## Common runbook scenarios

### "no traces in Jaeger"

1. `docker compose ps` — is `jaeger` healthy?
2. `docker logs goLearn-api-1` — did `observability init` log the
   exporter mode? Should say `service.name=goLearn-api`.
3. If env points at `localhost:4317`, the api is in a container — it
   needs `jaeger:4317`. Check `compose.full.yaml`.
4. Confirm propagator: a missing W3C `traceparent` header from the
   client means the api creates a fresh trace per request (still
   shows up; just not linked to upstream).

### "consumer stuck on a poison message"

```bash
make nats-cli
nats consumer info USERS user-welcome
# If `Pending ACKs > 0` and `Redelivered > MaxDeliver`, the message
# went to dead-letter (no DLQ subject configured today; it's just
# dropped after MaxDeliver=5).
```

Mitigations:

- Bad payload → consumer calls `msg.Term()` to skip permanently.
- Transient DB error → `msg.NakWithDelay(2s)`; redelivers up to 5x.
- App-level dedup → `INSERT ... ON CONFLICT DO NOTHING` makes
  redelivery a no-op so repeated failures don't double-write.

### "api container restarts in a loop"

`docker logs golearn-api-1` and look for the first error line. Common
causes:

- `migrate` job exited 1 → the api waits forever on
  `service_completed_successfully` and never starts. Inspect the
  migrate logs: `docker compose -f compose.full.yaml logs migrate`.
- `DATABASE_URL` typo → repo init returns and `run() error` exits 1.
- Port conflict on host → another process is already on `:8080`.
  Free it or change the host-side mapping in compose.

### "jetstream healthcheck fails"

The `wget /healthz?js-enabled-only=true` probe needs the JetStream
file store (`-sd /data`) to finish initialising. On slow disks the
default 10-retry × 5 s window can be too short.

Increase `retries:` on the healthcheck or pre-create the volume with
`docker volume create golearn_jsdata`.

## What to monitor in production

- **HTTP**: p50/p95/p99 latency on `/users` POST. Watch the
  `pgx INSERT users` span — long tail indicates DB contention.
- **NATS**: stream `Bytes` and `Messages` under MaxAge. If close to
  MaxBytes the broker drops oldest messages — by design, but worth
  alerting.
- **Consumer lag**: `Pending` count on `user-welcome`. >0 sustained
  means the consumer can't keep up; scale replicas (durable cursor
  is server-side, multiple replicas share work).
- **DB conn pool**: `pgxpool` `AcquireDuration` p99 — climbing means
  the pool is saturated.

## Test gates (CI-equivalent local runs)

```bash
make test                   # unit only (-short respected by tests)
make test-race              # full unit suite under -race
make test-integration       # E2E with testcontainers (Docker required)
make cover-gate             # filtered coverage; fails < 70%
make lint                   # golangci-lint
```
