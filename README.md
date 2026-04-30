# Day 2 — Concurrent CSV User Ingestion

A worker-pool ingestion pipeline built around a Processor strategy
interface. Multiple files run in parallel, the pool resizes at runtime
via an interactive REPL, duplicates are deduped across files,
individual files can be cancelled mid-flight without affecting others,
and the whole thing is race-detector clean.

## Topics exercised

Goroutines · channels · `sync.WaitGroup` · `sync.Mutex` · `sync/atomic`
· `context` propagation · the race detector · strategy pattern.

## Layout

```
cmd/
  gen/        generates sample CSVs with cross-file duplicates
  ingest/     CLI entrypoint: flags + REPL + signal handling
internal/
  user/       User struct + concurrency-safe AddIfNew store (dedup gate)
  processor/  Processor interface + Registry + CSVProcessor (strategy pattern)
  pool/       worker pool with runtime AddWorker/RemoveWorker, atomic stats
  ingest/     file/folder discovery + per-file driver + per-file cancel registry
```

Module path: `github.com/ashishsinghbhadoria/goLearn`. Go 1.22, zero
third-party dependencies.

---

## Quick start

```bash
# 1. Build the race-instrumented binary once (avoids re-compile per run).
go build -race -o /tmp/ingest_race ./cmd/ingest

# 2. Generate sample fixtures (4 files × 250 rows = 1000 records, ~15% dups).
go run ./cmd/gen -files 4 -rows 250 -dup 15

# 3. Folder ingest with default work mock (10–500ms per record).
/tmp/ingest_race -workers 8 -queue 64 data/

# 4. Per-file cancellation demo.
/tmp/ingest_race -workers 8 -repl=false \
  -cancel users_b.csv -cancel-after 50ms data/

# 5. Single file is also accepted, alongside or instead of folders.
/tmp/ingest_race -workers 4 data/users_a.csv
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
| `-workers` | `8` | Initial worker count. Resize at runtime via REPL. |
| `-queue` | `64` | Buffered job channel capacity (backpressure). |
| `-format` | `csv` | Processor name (must be registered). |
| `-repl` | `true` | Interactive REPL on stdin. Set `false` for batch runs. |
| `-cancel` | `""` | Comma-separated file basenames to auto-cancel mid-flight (demo). |
| `-cancel-after` | `30ms` | Delay before auto-cancellation fires. |
| `-work-min` | `10ms` | Minimum mock-work duration per record. |
| `-work-max` | `500ms` | Maximum mock-work duration per record. |

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
| `cancel <name>` | Cancel the per-file context for the file whose basename matches. Other files unaffected. |
| `quit` / `exit` | Cancel rootCtx (everything shuts down cleanly). |

Example:

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
`reg.Register(JSONProcessor{})` — pool, runner, and REPL stay
unchanged. The CLI selects via `-format <name>`.

### Dynamic worker pool

Each worker holds its own `quit chan struct{}`. The pool keeps the
live workers in a slice guarded by `sync.Mutex`; resize ops are O(1):

```go
func (p *Pool) AddWorker() int                  // append + spawn goroutine
func (p *Pool) RemoveWorker() (int, error)      // pop + close(quit)
```

The worker loop selects across:

```go
case <-w.quit:           // RemoveWorker — graceful exit
case <-p.stop:           // Pool.Stop()  — shutdown
case <-rootCtx.Done():   // SIGINT / quit — cancel everything
case j := <-p.jobs:      // process record
```

`Submit` is select-based too (against the same signals), so it never
panics on a stopped pool and always invokes `j.Done()` on its error
paths so the caller's WaitGroup releases.

### Cancellation hierarchy

```
rootCtx (signal.NotifyContext: SIGINT/SIGTERM)
  ├── per-file ctx 1 ──► passed into Processor.Stream + every Job
  ├── per-file ctx 2
  └── per-file ctx N
```

The runner stores per-file `CancelFunc`s by basename so the REPL can
cancel a single file. The mock `ProcessRow` does an interruptible
`select`-on-timer-and-ctx — without that, per-file cancel would lag
by up to one full sleep per in-flight record.

### Dedup

`user.Store.AddIfNew` is a check-then-insert under one mutex, keyed
by email. Dedup runs *inside the worker* (not at submit time) so a
record cancelled while still in the queue never pollutes the Store.

### Race-free guarantees

| State                   | Guard                          |
|-------------------------|--------------------------------|
| `Store.users` map       | `sync.Mutex`                   |
| Pool worker slice       | `sync.Mutex`                   |
| Job dispatch            | `chan Job` (buffered)          |
| Stop signalling         | `chan struct{}` (close once)   |
| Pool counters           | `sync/atomic.Uint64`           |
| Per-worker counts       | `sync.Map[int]*atomic.Uint64`  |
| Per-file cancel map     | `sync.Mutex`                   |
| Cancellation cascade    | `context.Context` parent/child |

Verified with `go run -race ./cmd/ingest …` — no race reports across
the full benchmark matrix below.

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

### Baseline throughput (8 workers)

| Records | Wall | ok | dedup | Throughput | Per-worker spread |
|---|---|---|---|---|---|
| 10 | 363 ms | 10 | 0 | 28 rec/s | 1–2 |
| 1 000 | 29.4 s | 898 | 102 | 34 rec/s | 117–137 |
| 10 000 | 6.37 s | 8 882 | 1 118 | **1 570 rec/s** | 1 214–1 295 |
| 1 000 000 | 9.44 s | 887 465 | 112 535 | **105 900 rec/s** | 124 774–125 234 (0.18 % spread) |

The channel-fed pool load-balances naturally — whichever worker is
idle pulls the next job, so per-worker counts stay tight.

### Add / remove worker mid-flight

| Records | Initial | Resize | Wall | Result |
|---|---|---|---|---|
| 1 000 | 8 | +4 @ 5 s → 12, −6 @ 15 s → 6 | 26.7 s | LIFO removal verified; w7=63, w8=68; w9–w12=42–46 (only existed 5–15 s) |
| 10 000 | 4 | +4 @ 1 s → 8, −4 @ 4 s → 4 | 9.7 s | w1–w4=1 900–1 940 (full run); w5–w8=560–593 (1–4 s window) |
| 1 000 000 | 4 | +4 @ 1 s → 8, −4 @ 4 s → 4 | 15.1 s | w1–w4=210k–212k; w5–w8=38.5k–38.7k |

Every run has `ok + dedup` matching the baseline exactly — no records
dropped on resize. Removed workers finish their in-flight job before
exiting (graceful drain).

### Cancel one file mid-flight

| Records | Cancel @ | Cancelled file's records | Cancelled file's wall | Other files' wall |
|---|---|---|---|---|
| 1 000 | 5 s | 68 / 250 | 6.6 s | 23.7–24.8 s |
| 10 000 | 1 s | 429 / 2 500 | 1.13 s | 5.18–5.20 s |
| 1 000 000 | 1 s | 24 970 / 250 000 | 1.01 s | 7.59 s |

The targeted file's wall closes within ~10 ms of `cancel-after` —
the cancel-aware `select` in `MockProcessRow` makes in-flight
records bail immediately. Other files run to completion as if the
cancel never happened.

---

## Edge cases & known limitations

The current code prioritises clarity over hardening. The most
important sharp edges, in roughly decreasing priority:

### Data correctness (silent corruption — fix first)

1. **Header column order is positional, not named.** A file with header
   `name,id,email` would silently swap fields — name becomes the ID,
   ID becomes the name. Mitigation: validate the header tokens, or
   build a name→index map from the header instead of indexing
   positionally. ([internal/processor/csv.go](internal/processor/csv.go))

2. **UTF-8 BOM is not stripped.** A file written with a BOM leaks
   `﻿` into `row[0]` of the first record. Mitigation: peek-and-strip
   the BOM in the Processor before wrapping `csv.NewReader`.

3. **Email keys are not normalised.** `User@Example.com` and
   `user@example.com ` (trailing space) hash to different keys, so dedup
   silently misses them. Mitigation: normalise in `AddIfNew`
   (`strings.ToLower(strings.TrimSpace(...))`).

4. **Empty email** (or rows with empty email field) all dedup to the
   same key. Mitigation: reject empty-email rows at the parser or the
   Store boundary.

### Concurrency / API misuse

5. **`-workers 0` freezes ingestion.** `Submit` will block forever
   waiting for a worker. Mitigation: validate `*workers >= 1` after
   flag parse.

6. **Negative `-queue` panics** on `make(chan Job, -1)`. Mitigation:
   clamp/validate in `pool.New`.

7. **Stop must be called after all Submitters have stopped.** Calling
   `Stop()` while a goroutine is mid-`Submit` can race the post-Stop
   drain and leak a job (Done never called → producer's WaitGroup
   hangs). Today's code path is safe (`runner.Run` returns before
   `Stop`), but the contract isn't documented. Mitigation: document
   on `Stop`, or count in-flight Submits with an `atomic.Int64`.

8. **Stuck FIFO / slow NFS as input.** Go's `ctx.Done()` does not
   interrupt a blocked file-read syscall, so a stuck pipe leaks the
   producer goroutine. Mitigation: wrap the file with a context-aware
   reader that calls `f.Close()` on `ctx.Done()` to force the read
   to return EBADF.

9. **Duplicate basenames across folders** — e.g. `a/users.csv` and
   `b/users.csv`. `runner.cancels` keys by basename, so the second
   overwrites the first; cancel-by-basename only hits one. Mitigation:
   key by full path internally, accept basename in the REPL with a
   "first match wins" or "ambiguous" rule.

### Scale / resource

10. **Per-record `log.Printf` is the bottleneck above ~100k rec/s.**
    The stdlib log mutex serialises every line. Mitigation: switch to
    `log/slog` with a buffered handler, gate per-record logs behind
    `-verbose`, or sample 1-in-N.

11. **Store memory grows unbounded** — at 1 B unique records you'd
    need ~100 GB. Mitigation: pluggable backend (Redis / SQLite),
    Bloom filter for fast-path checks, or sharded in-memory.

12. **Single `Store.mu` mutex** becomes contended above ~1 M rec/s.
    Mitigation: shard the map (`hash(email) % N` stripes) or use
    `sync.Map` (works less well for write-heavy workloads).

13. **No recursive folder traversal.** Only top-level files match.
    Sub-folders silently ignored. Mitigation: `-recursive` flag using
    `filepath.WalkDir`.

14. **Massive single CSV row** (multi-MB quoted field) will allocate
    unbounded memory in `csv.Reader`. Mitigation: `io.LimitReader`
    around the file with a sane per-record cap.

15. **REPL `add 1000000`** has no upper bound. Mitigation: cap at
    e.g. 10× initial `*workers`.

---

## Extending: adding a new format

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

Then run:

```bash
ingest -format json data_jsonl/
```

No changes to the pool, runner, REPL, store, or CLI flags.
