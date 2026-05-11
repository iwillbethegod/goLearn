package ingest

import (
	"context"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/processor"
)

// FileStats is re-exported from the model package so existing
// callers can keep saying `ingest.FileStats`. The canonical type
// lives there alongside other data-bearing structs.
type FileStats = model.FileStats

// FileGate is an optional pre/post-file hook. cmd/ingest plugs in a
// gRPC token gate via WithGate so each file is rate-limited against
// the user's bucket. The Runner stays unaware of gRPC or tokens.
//
// BeforeFile returns the number of rows the gate has reserved for
// the file. A non-nil error skips the file. AfterFile is called
// once the file has finished (or been cancelled) with the row count
// originally reserved and the count that actually finished as
// OutcomeOK / OutcomeDedup. Implementations typically refund
// (reserved − handled) tokens.
type FileGate interface {
	BeforeFile(ctx context.Context, path string) (reserved int64, err error)
	AfterFile(ctx context.Context, path string, reserved, handled int64)
}

// Runner drives multiple files through one Processor and one Pool in
// parallel. It applies the same handler chain to every record and
// tracks per-file cancellation funcs by basename so the REPL can
// target individual files.
type Runner struct {
	proc   processor.Processor
	pool   *pool.Pool
	chain  handler.Handler
	stats  *handler.Stats
	logger *slog.Logger
	gate   FileGate

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	files   []FileStats
}

func NewRunner(proc processor.Processor, p *pool.Pool, chain handler.Handler, stats *handler.Stats, logger *slog.Logger) *Runner {
	return &Runner{
		proc:    proc,
		pool:    p,
		chain:   chain,
		stats:   stats,
		logger:  logger,
		cancels: make(map[string]context.CancelFunc),
	}
}

// WithGate registers a per-file rate-limiting hook. nil disables the
// gate (default). Must be called before Run.
func (r *Runner) WithGate(g FileGate) { r.gate = g }

// CancelFile cancels the per-file context for basename. Returns false
// if no such file is currently active.
func (r *Runner) CancelFile(basename string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[basename]
	r.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (r *Runner) ActiveFiles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.cancels))
	for k := range r.cancels {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *Runner) Files() []FileStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FileStats, len(r.files))
	copy(out, r.files)
	return out
}

// Run streams every path through the processor and pool concurrently,
// returning when every file has finished (completed, cancelled, or
// gate-skipped).
func (r *Runner) Run(rootCtx context.Context, paths []string) {
	var fwg sync.WaitGroup
	for _, p := range paths {
		p := p
		fwg.Add(1)
		go func() {
			defer fwg.Done()
			r.runFile(rootCtx, p)
		}()
	}
	fwg.Wait()
}

func (r *Runner) runFile(rootCtx context.Context, path string) {
	name := filepath.Base(path)
	fctx, fcancel := context.WithCancel(rootCtx)
	defer fcancel()

	r.mu.Lock()
	r.cancels[name] = fcancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.cancels, name)
		r.mu.Unlock()
	}()

	// Pre-flight: reserve tokens. A non-nil err here means the file
	// is skipped entirely (e.g. insufficient tokens).
	var reserved int64
	if r.gate != nil {
		var err error
		reserved, err = r.gate.BeforeFile(fctx, path)
		if err != nil {
			r.logger.Warn("file skipped by gate", "file", name, "err", err)
			return
		}
	}

	start := time.Now()
	stream, err := r.proc.Stream(fctx, path)
	if err != nil {
		r.logger.Error("file open failed", "file", name, "err", err)
		if r.gate != nil {
			// Refund everything — file never started.
			r.gate.AfterFile(fctx, path, reserved, 0)
		}
		return
	}

	var jobs sync.WaitGroup
	var handled atomic.Int64 // ok + dedup outcomes
	streamed := 0

	for rec := range stream {
		if rec.Err != nil {
			r.stats.IncParseErr()
			r.logger.Warn("parse error", "file", name, "err", rec.Err)
			continue
		}
		jobs.Add(1)
		u := rec.User
		err := r.pool.Submit(fctx, func(ctx context.Context) {
			defer jobs.Done()
			out := r.chain(ctx, name, u)
			switch out {
			case handler.OutcomeOK, handler.OutcomeDedup:
				handled.Add(1)
			}
		})
		if err != nil {
			// Submit didn't enqueue — the closure never runs, so
			// release the WaitGroup ourselves.
			jobs.Done()
		}
		streamed++
	}
	jobs.Wait()
	dur := time.Since(start)
	handledN := handled.Load()

	if r.gate != nil {
		r.gate.AfterFile(fctx, path, reserved, handledN)
	}

	r.mu.Lock()
	r.files = append(r.files, FileStats{
		Path:     path,
		Records:  streamed,
		Handled:  int(handledN),
		Duration: dur,
	})
	r.mu.Unlock()

	r.logger.Info("file done",
		"file", name,
		"records", streamed,
		"handled", handledN,
		"duration", dur,
	)
}
