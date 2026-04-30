# Day 2 — Concurrent CSV User Ingestion

A worker-pool ingestion pipeline that consumes multiple CSV files in parallel,
deduplicates user records across files, supports per-file cancellation, and is
race-detector clean.

## Topics exercised

Goroutines · channels · `sync.WaitGroup` · `sync.Mutex` · `context`
propagation · the race detector.

## Layout

```
cmd/
  gen/      generates sample CSVs with configurable cross-file duplicates
  ingest/   runs the worker pool against one or more CSVs
internal/
  user/     User struct + concurrency-safe AddIfNew store (dedup gate)
  csvr/     context-aware CSV streaming reader
  pool/     fixed-size worker pool with per-job context routing
```

## How to run

```bash
# 1. Generate 4 CSVs × 250 rows = 1000 jobs, ~15% cross-file dups
go run ./cmd/gen -files 4 -rows 250 -dup 15

# 2. Ingest with the race detector enabled
go run -race ./cmd/ingest -workers 8 \
  data/users_a.csv data/users_b.csv data/users_c.csv data/users_d.csv

# 3. Cancel one file mid-flight; the others continue unaffected
go run -race ./cmd/ingest -workers 8 \
  -cancel users_b.csv -cancel-after 20ms \
  data/users_a.csv data/users_b.csv data/users_c.csv data/users_d.csv
```

## Concurrency approach

**One pool, many producers.** A single `Pool` of N workers reads from one
buffered `chan Job`. Each CSV file runs in its own producer goroutine that
streams rows and submits jobs. The pool is started once and stopped once;
adding more files just means more producers — no re-tuning of worker count.

**Per-file context, propagated through each job.** Every file derives its own
`context.Context` from the root context. That file context is attached to
every `Job` it submits. Workers check three signals in order:

1. root `ctx.Done()` → drain and exit
2. `job.Ctx.Done()` → skip this row, keep the worker alive for other files
3. mid-work cancellation via `select` against `time.After` simulating I/O

This is what makes "cancel one file without affecting others" cheap: a file
cancel only marks that file's already-queued jobs as no-ops; jobs from other
files keep flowing through the same worker pool.

**Cross-file dedup as an atomic check-and-set.** `user.Store.AddIfNew` holds a
`sync.Mutex` for the entire `if exists { return false } else { insert }` block,
so two workers racing on the same email cannot both win. A duplicate becomes a
single `dedup` log line and a no-op — no double-write to the store.

**Per-file completion tracking.** The submitter creates a per-file
`sync.WaitGroup`, calls `Add(1)` before sending each job, and passes
`wg.Done` as the job's `Done` callback. Workers invoke `Done` exactly once
in `defer`, regardless of outcome (processed / dedup / cancelled). The
producer waits on this WG to log accurate per-file elapsed time — capturing
real processing time, not just streaming time.

**Race-free guarantees.** All shared state lives behind well-defined
synchronisation:

| State                   | Guard                      |
|-------------------------|----------------------------|
| `Store.users` map       | `sync.Mutex`               |
| Job dispatch            | `chan Job` (buffered)      |
| Worker lifecycle        | `sync.WaitGroup` in pool   |
| Per-file completion     | per-file `sync.WaitGroup`  |
| Cancellation signalling | `context.Context` cascade  |

Verified with `go run -race ./cmd/ingest …` — no race reports.

## Sample benchmark (1000 jobs, 8 workers, M-series Mac)

```
[main] FILE    file=data/users_c.csv streamed=250 elapsed=228.6ms
[main] FILE    file=data/users_b.csv streamed=250 elapsed=246.9ms
[main] FILE    file=data/users_a.csv streamed=250 elapsed=255.9ms
[main] FILE    file=data/users_d.csv streamed=250 elapsed=258.3ms
[main] done: stored=898 total=258.4ms
```

Per-worker job counts (sample run, 1000 jobs):

```
worker 0 → 125    worker 4 → 124
worker 1 → 133    worker 5 → 124
worker 2 → 125    worker 6 → 122
worker 3 → 124    worker 7 → 123
```

Distribution is even (≈125 ±5) because the channel naturally load-balances:
whichever worker is idle pulls the next job.

## Cancellation demo (one file killed at 20 ms)

```
[main] CANCEL  file=data/users_b.csv after=20ms
[worker 1] cancelled file=users_b.csv email=user258@example.com (mid-work)
... (remaining users_b rows skipped) ...
[main] FILE    file=data/users_b.csv streamed=26  elapsed=34.2ms   ← cancelled
[main] FILE    file=data/users_c.csv streamed=250 elapsed=199.7ms  ← unaffected
[main] FILE    file=data/users_a.csv streamed=250 elapsed=211.9ms  ← unaffected
[main] FILE    file=data/users_d.csv streamed=250 elapsed=211.7ms  ← unaffected
[main] done: stored=720 total=212.1ms
```

Cancelling `users_b.csv` stops its CSV reader immediately, drops in-flight
rows from that file, and lets the other three finish normally on the same
worker pool.
