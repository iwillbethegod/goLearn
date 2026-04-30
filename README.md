# Day 2 — Concurrent CSV User Ingestion

A worker-pool ingestion pipeline built around a Processor strategy
interface and a middleware-based handler chain. Multiple files run in
parallel, the pool resizes at runtime via an interactive REPL,
duplicates are deduped across files, individual files can be
cancelled mid-flight without affecting others, and the whole thing is
race-detector clean.

## Topics exercised

Goroutines · channels · `sync.WaitGroup` · `sync.Mutex` · `sync/atomic`
· `context` propagation · the race detector · strategy pattern ·
middleware chain · `log/slog` · functional options.

## Layout

```
cmd/
  gen/        generates sample CSVs with cross-file duplicates
  ingest/     CLI entrypoint (main.go), config + flag parsing,
              summary printer, and the ProcessRow mock fixture
internal/
  user/       User struct + concurrency-safe AddIfNew store
  processor/  Processor interface + Registry + CSVProcessor
              (strategy pattern over input format)
  pool/       Domain-agnostic worker pool: Submit(ctx, fn). Workers
              inject their ID via context value; nothing else.
  handler/    Per-record pipeline: Outcome, Handler, Middleware,
              Chain, Stats/Snapshot, and the middleware constructors
              (CancelCheck, Dedup, Process, Metrics, Logging,
              PerWorkerCount).
  ingest/     File/folder discovery + per-file driver. Submits one
              closure per record into the pool; closures invoke the
              handler chain. Tracks per-file cancel funcs by basename.
  repl/       Stdin command interface (add/remove/status/files/cancel/
              quit). Depends only on small interfaces, not concrete
              pool / runner / store types.
```

Module path: `github.com/ashishsinghbhadoria/goLearn`. Go 1.22, zero
third-party dependencies.

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

## Quick start

```bash
# 1. Build the race-instrumented binary once.
go build -race -o /tmp/ingest_race ./cmd/ingest

# 2. Generate fixtures (4 files × 250 rows = 1000, ~15% dups).
go run ./cmd/gen -files 4 -rows 250 -dup 15

# 3. Folder ingest with default work mock (10–500ms per record).
/tmp/ingest_race -workers 8 -queue 64 data/

# 4. Per-file cancellation demo.
/tmp/ingest_race -workers 8 -repl=false \
  -cancel users_b.csv -cancel-after 50ms data/

# 5. High-throughput run with sub-millisecond mock work.
/tmp/ingest_race -workers 8 -queue 1024 -repl=false \
  -work-min 10us -work-max 100us data_1m/

# 6. Verbose mode — one structured log line per record.
/tmp/ingest_race -workers 4 -repl=false -verbose data_10/
```

You can pass any mix of files and folders. Folders are scanned
non-recursively for files matching the active processor's extension.

---

## CLI reference

```
ingest [flags] <path> [<path> ...]
```

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

### Processor strategy

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
