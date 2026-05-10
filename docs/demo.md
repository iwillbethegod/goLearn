# 5–10 Minute Demo Script

A timed walk-through of the goLearn capstone. Anyone with Docker
installed can replay this end-to-end.

## Pre-flight (do not record)

```bash
git clone https://github.com/iwillbethegod/goLearn && cd goLearn
docker --version    # 24+
make build vet      # ensure local toolchain works
```

---

## 0. Cold open · 60 s

> "goLearn is a user-management service built across 7 days. Today you
> see one command boot the entire stack: Postgres, NATS JetStream,
> Jaeger, the REST+gRPC api, and a separate event consumer. We'll
> create a user, watch the event flow through NATS, and trace the
> entire call across services as a single trace_id."

Show the architecture diagram in `docs/architecture.md` (the layered
ASCII view).

---

## 1. One-command startup · 90 s

```bash
make stack-up
```

Watch the logs:

```bash
make stack-logs
```

Talking points while containers come up:

- `migrate` job runs once and exits 0 — visible in the logs.
- `api` and `consumer` only start *after* migrate succeeds and NATS
  is healthy (note the `service_completed_successfully` and the
  JetStream-aware `/healthz?js-enabled-only=true` probe).
- Distroless images: ~20 MB each, no shell, run as nonroot.

Verify everything is up:

```bash
docker compose -f compose.full.yaml ps
```

---

## 2. Create a user (cross-service flow) · 90 s

```bash
curl -s -X POST localhost:8080/users \
  -H "content-type: application/json" \
  -d '{"name":"Ada","email":"ada@example.com","password":"hunter22"}' | jq
```

Show the slog line on `api`:

```bash
docker compose -f compose.full.yaml logs api | grep "user registered"
# Look for:  trace_id=... span_id=... user_id=u-...
```

And the matching consumer line:

```bash
docker compose -f compose.full.yaml logs consumer | grep "user.created processed"
# Same trace_id — proves W3C trace context propagated through NATS.
```

Talking point: a single `trace_id` spans the entire flow without any
manual instrumentation in the application code — the publisher
inserts `traceparent` into NATS headers, the consumer extracts it
before starting its span.

---

## 3. Inspect the trace in Jaeger · 120 s

Open `http://localhost:16686` in a browser.

1. Service: `goLearn-api` → search.
2. Click any trace.
3. Show the span tree:

```
POST /users                                 ← otelhttp
└─ user.Service.Register                    (manual via stdlib)
   ├─ pgx.exec INSERT users                 ← otelpgx
   ├─ pgx.exec INSERT registration_log      ← otelpgx
   └─ nats publish user.created             ← manual W3C inject
       │
       └─ consumer.user.created             ← manual W3C extract
           └─ pgx.exec INSERT notifications ← otelpgx
```

Talking points:
- 6 spans, 1 trace_id.
- Look at the gap between the producer and consumer span — that's
  NATS broker latency (typically <5 ms for JetStream local).
- Click each span to see the full attributes (`service.name`,
  `messaging.message.id`, `db.statement`, etc.).

---

## 4. Idempotency under redelivery · 90 s

> "JetStream redelivers on Nak or AckWait expiry. Show that
> redelivery doesn't double-write."

```bash
psql "$DATABASE_URL" -c 'SELECT count(*) FROM notifications;'
# 1
```

Now re-publish the same `event_id`:

```bash
make nats-cli
# inside nats-box:
nats pub user.created '{"event_id":"<paste from notifications row>",...}' \
    -H Nats-Msg-Id:'<same event_id>'
exit
```

```bash
psql "$DATABASE_URL" -c 'SELECT count(*) FROM notifications;'
# Still 1 — INSERT ... ON CONFLICT (event_id) DO NOTHING.
```

Talking point: server-side dedup window catches duplicates within ~2
minutes; the UNIQUE(event_id) + ON CONFLICT in the consumer's INSERT
catches the rest forever.

---

## 5. Graceful shutdown on SIGTERM · 60 s

```bash
make stack-down
```

Watch the logs scroll: each container exits cleanly within the 30 s
compose grace window. No "killed" messages.

Talking points:
- `signal.NotifyContext` flips the root ctx.
- HTTP `Shutdown(ctx)` drains in-flight requests.
- gRPC `GracefulStop` lets active RPCs finish.
- NATS `Drain` flushes pending acks.
- pgxpool `Close` waits for active queries.
- TracerProvider `Shutdown` flushes the last batch of spans —
  visible in Jaeger even after the api container exited.

---

## 6. CI + image pipeline · 60 s

Open the GitHub repo in the browser:

```bash
gh repo view --web
```

1. Click "Actions" → show the latest CI run.
   - 3 jobs: `vet+test+coverage`, `lint`, `test-integration`.
   - Coverage gate at 70% — show the green check.
2. Click "Packages" → show `golearn-api:latest` and
   `golearn-consumer:latest` on GHCR.
   - Tags: `latest` + commit SHA.
   - Multi-stage distroless image, ~20 MB.

Anyone can pull and run:

```bash
docker pull ghcr.io/iwillbethegod/golearn-api:latest
docker run -e DATABASE_URL=... ghcr.io/iwillbethegod/golearn-api:latest
```

---

## 7. Wrap-up · 60 s

> "What the 7 days bought us:
>
> - **One service** with two transport surfaces (REST + gRPC), three
>   persistence backends (memory / jsonfile / postgres), event-driven
>   fan-out, and full distributed tracing.
> - **One trace_id** spanning every layer.
> - **One command** to boot the entire stack.
> - **70%+ test coverage** enforced in CI.
> - **CI/CD pipeline** that gates every PR on `vet → test → lint →
>   integration → coverage`, and ships container images on every merge
>   to main.
>
> Future work I'd prioritise: the transactional outbox pattern for
> the dual-write between Postgres and NATS, OIDC auth on the REST and
> gRPC surfaces, and arm64 image builds when we have a native runner."

---

## Cheat sheet for the demo terminal

Pre-set these aliases / windows:

```bash
# Window 1: stack lifecycle
make stack-up
make stack-logs

# Window 2: api logs
docker compose -f compose.full.yaml logs -f api

# Window 3: consumer logs
docker compose -f compose.full.yaml logs -f consumer

# Window 4: psql
psql "$DATABASE_URL"
\watch select count(*) from notifications;

# Window 5: NATS CLI
make nats-cli

# Browser tabs:
#   localhost:16686  (Jaeger)
#   github.com/<repo>/actions
#   github.com/<repo>/packages
```
