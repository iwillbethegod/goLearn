# REST vs gRPC

Both transports talk to the **same** `user.Service` in this repo. Day 3
shipped REST (`cmd/api` HTTP listener); day 4 added gRPC (`cmd/api`
gRPC listener) so the CSV processor (`cmd/ingest`) could enforce
per-user token quotas without going through HTTP. This note captures
when to use which.

## Side-by-side

|  | REST (Day 3) | gRPC (Day 4) |
|---|---|---|
| Wire format | JSON (text) | Protobuf (binary) |
| Transport | HTTP/1.1 | HTTP/2 |
| Schema | OpenAPI 3.0 (`api/openapi.yaml`); validation via `kin-openapi` middleware | `.proto` (`proto/user/v1/user.proto`); validation by the type system |
| Codegen | `oapi-codegen` → server interface + types | `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` → server interface, types, and a typed client |
| Generated Go file size for our 5-RPC contract | ~520 lines (`server.gen.go`) | ~250 lines (`user.pb.go` + `user_grpc.pb.go`) |
| Client experience | `curl`, Postman, `fetch`, ad-hoc HTTP libs everywhere | Generated typed client; `grpcurl` for ad-hoc |
| Per-call overhead at p50 | ~0.5 ms (JSON parse + HTTP/1.1 framing) | ~10–100 µs (binary marshal + HTTP/2 multiplexing) |
| Streaming | Server-sent events / chunked HTTP (awkward) | Native unary, server-streaming, client-streaming, and bidi |
| Browser-native | Yes (cross-origin with CORS) | No (needs grpc-web proxy) |
| Errors | HTTP status code + JSON envelope `{code, message}` | `grpc.Status` with `codes.NotFound` etc. + structured details |
| Auth | `Authorization` header (Bearer/Basic) | gRPC metadata; mTLS very common |

## Where each shines in this repo

- **REST** is the **public-facing surface**. Anyone with `curl` or
  Postman can integrate. The `api/openapi.yaml` doubles as machine-
  readable docs and is what we'd ship to external integrators. The
  Day-3 endpoints — `POST /users`, `GET /users`, `GET/PUT/DELETE
  /users/{id}` — are intentionally there for human + ad-hoc tooling.

- **gRPC** is the **internal service-to-service** channel. The
  CSV processor calls `TakeTokens` and `ReturnTokens` on every file —
  hot-path RPCs that benefit from the binary format and strong
  typing. The contract is shared via the `.proto`, so the server
  and client can never disagree on field names or types. Day 4's
  token-bucket coordination is exactly the kind of fast,
  high-frequency call where gRPC is the right tool.

## Concrete example: the same User read

**REST (`GET /users/{id}`):**

```bash
curl -s -i http://localhost:8080/users/u-7c0a5b6d3f1e9a4b2d8c4f1e
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-Id: 9c6f5f18d9f2b2cc

{"id":"u-7c0a5b6d3f1e9a4b2d8c4f1e","name":"Alice","email":"alice@example.com"}
```

Wire size: ~110 bytes including the JSON.

**gRPC (`UserService.GetUser`) via `grpc-demo`:**

```bash
go run ./cmd/grpc-demo -addr :9090 -get u-7c0a5b6d3f1e9a4b2d8c4f1e
id=u-7c0a5b6d3f1e9a4b2d8c4f1e name=Alice email=alice@example.com
```

Wire size: ~70 bytes (binary protobuf). The Go client side sees
`*userpb.User{Id, Name, Email}` directly — no JSON parse, no
intermediate `map[string]any`.

## Schema evolution

Both formats handle adding fields gracefully:

- **OpenAPI**: add a property to the schema → unknown fields in
  responses are ignored by old clients; required new fields would
  break old clients (so don't make them required).
- **Protobuf**: add a new field with a new tag number → old clients
  ignore it on read, write zero values on encode. Removing or
  renumbering fields is forbidden — the `// reserved 5;` syntax
  enforces that at compile time.

Protobuf's discipline here is stricter: `protoc` will refuse to
generate code for a renumbered or duplicate-tagged field, so the
contract drifts less in practice.

## Day-4-specific: why gRPC for tokens

The CSV processor calls the user service **twice per file** —
once to reserve, once to refund any unconsumed slack. With four
files in the day-2 demo dataset, that's 8 RPCs per ingest run. At
production scale, picture 1000 files → 2000 RPCs. The savings:

- No JSON parse on the hot path.
- HTTP/2 multiplexes all 2000 RPCs over a single connection.
- The Go client is a generated `userpb.UserServiceClient` —
  compile-time-checked against the same `.proto` the server uses.

If we'd reused REST for this, we'd be paying ~5× the overhead and
juggling string-keyed JSON maps in code that's already busy
processing 100k records/sec.

## What we kept in REST

External-facing CRUD. Day 3's `cmd/api` REST endpoints are the
documented integration surface. The gRPC service exposes the same
CRUD ops too (`GetUser`, `ListUsers`) for symmetry and as Day-4
demo material — but in a real product split, REST stays public,
gRPC stays internal.
