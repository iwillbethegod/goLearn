package ingest

import (
	"context"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
	"github.com/ashishsinghbhadoria/goLearn/internal/processor"
)

// FileStats summarises one file's lifecycle. Records is the count
// emitted by the Processor (parse errors excluded).
type FileStats struct {
	Path     string
	Records  int
	Duration time.Duration
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
// returning when every file has finished (completed or cancelled).
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

	start := time.Now()
	stream, err := r.proc.Stream(fctx, path)
	if err != nil {
		r.logger.Error("file open failed", "file", name, "err", err)
		return
	}

	var jobs sync.WaitGroup
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
			r.chain(ctx, name, u)
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

	r.mu.Lock()
	r.files = append(r.files, FileStats{Path: path, Records: streamed, Duration: dur})
	r.mu.Unlock()

	r.logger.Info("file done", "file", name, "records", streamed, "duration", dur)
}
