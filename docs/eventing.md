# Event-Driven Design (Day 6)

The Day-6 layer turns the user service into an event source. Every
successful registration fans out a `user.created` event onto NATS
JetStream; a separate `cmd/consumer` binary reacts to each event by
writing a row into the `notifications` table.

```
┌──────────────┐  POST /users           ┌──────────────────┐
│   client     │ ─────────────────────▶ │ cmd/api          │
└──────────────┘                        │  user.Service    │
                                        │   ├── repo.Add   │ ──▶ Postgres `users`
                                        │   └── publisher  │
                                        │       PublishUserCreated
                                        └──────┬───────────┘
                                               │ JetStream
                                               ▼
                                        ┌──────────────────┐
                                        │ NATS: USERS      │
                                        │  subject:        │
                                        │  user.created    │
                                        └──────┬───────────┘
                                               │ pull (durable: user-welcome)
                                               ▼
                                        ┌──────────────────┐
                                        │ cmd/consumer     │
                                        │  Handle()        │
                                        │   └── pgdb.Insert │ ──▶ Postgres `notifications`
                                        └──────────────────┘
```

## Stream + consumer config

`internal/events/nats` ensures the broker has the right shape on
publisher start (idempotent — `CreateOrUpdateStream`):

| Field        | Value             | Reason                                                |
| ------------ | ----------------- | ----------------------------------------------------- |
| Stream name  | `USERS`           | One stream per domain entity                          |
| Subjects     | `user.>`          | Reserve the namespace for `user.deleted` etc.         |
| Retention    | Limits            | Drop old events after MaxAge / MaxBytes               |
| Storage      | File              | Survives broker restarts                              |
| MaxAge       | 24 h              | Bounded retention                                     |
| MaxBytes     | 1 GiB             | Bounded volume                                        |

`cmd/consumer` then ensures a durable pull consumer (idempotent —
`CreateOrUpdateConsumer`):

| Field            | Value                | Reason                                          |
| ---------------- | -------------------- | ----------------------------------------------- |
| Durable          | `user-welcome`       | Survives consumer restarts; cursor is server-side |
| FilterSubject    | `user.created`       | One handler per subject                         |
| AckPolicy        | AckExplicit          | Each message acked individually                 |
| AckWait          | 30 s                 | Redelivery if handler stalls / crashes          |
| MaxDeliver       | 5                    | Move to DLQ shape after 5 failed attempts       |

## Wire schema (`user.created.v1`)

```json
{
  "event_id":    "5f3a7c8e1d04b9a2c08f6e3d1b4a5c7e",
  "schema":      "user.created.v1",
  "occurred_at": "2026-05-08T16:42:11.123456Z",
  "user": {
    "id":    "u-...24-hex...",
    "name":  "Ada Lovelace",
    "email": "ada@example.com"
  }
}
```

Hard rules:

- **`password_hash` is never present.** Events must not carry
  credentials. The publisher constructs the payload from `model.User`
  but explicitly omits the field.
- **`created_at` from the row is never present.** That's storage
  metadata; the event has its own `occurred_at`.
- **`schema` is pinned.** A breaking change ships on a new subject
  (`user.created.v2`) so old subscribers keep working.
- **`event_id` doubles as `Nats-Msg-Id`** (header). JetStream uses it
  for a server-side dedup window; consumers use it as an idempotency
  key when they fan out to side effects.

## Idempotency

JetStream redelivers on every Nak, AckWait expiry, or consumer
restart. The consumer's INSERT into `notifications` uses
`ON CONFLICT (event_id) DO NOTHING`, so a re-delivered message
becomes a silent no-op row insert. The integration test in
`internal/storage/postgres/notifications_test.go` proves this
contract end-to-end against a real Postgres.

## Trace propagation across NATS

The publisher injects W3C `traceparent` (and Baggage) into the NATS
message headers via OpenTelemetry's TextMap propagator. The consumer
extracts the same headers before starting its `consumer.user.created`
span, so the cross-service trace stitches together as one tree in
Jaeger. See `docs/observability.md` for the full picture.

## Dual-write caveat (Day-7 future work)

`Service.Register` does two writes: the DB row commits first, then
the NATS publish runs. If NATS is down at the moment of publish, the
user is created but no event fires. The `Register` handler logs the
publish error and returns success to the client (the user IS in the
DB; failing the response would be misleading).

Production-grade fix: the **transactional outbox pattern**. Write
the event row to an `events_outbox` table inside the same DB
transaction as `users`, then have a separate poller publish from
outbox to NATS and mark rows acked. The DB write commits only if
both `users` and `events_outbox` land; the publisher is decoupled
from the request lifecycle.

For now, the publisher uses a **detached publish ctx** so a *client
disconnect* between `repo.Add` and `js.PublishMsg` doesn't lose the
event:

```go
detached := trace.ContextWithSpanContext(
    context.WithoutCancel(parent),
    trace.SpanContextFromContext(parent),
)
pubCtx, cancel := context.WithTimeout(detached, 2*time.Second)
```

`WithoutCancel` drops cancellation. `ContextWithSpanContext` carries
the trace forward so the publish span stays attached to the same
trace_id.

## Operating

Local dev:

```bash
make compose-up        # starts db + nats + jaeger
make migrate-up        # applies migrations 000001 + 000002
make api-run           # cmd/api with -nats-url and -migrate
make consumer-run      # cmd/consumer (in another terminal)
make nats-cli          # then: `nats stream info USERS` etc.
```

Inspecting the broker:

```bash
nats stream info USERS
nats consumer info USERS user-welcome
nats stream view USERS
```
