package ingest

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"sync"
	"time"

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
// parallel. It tracks per-file cancellation funcs by basename so the
// REPL can target individual files.
type Runner struct {
	proc processor.Processor
	pool *pool.Pool

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	files   []FileStats
}

func NewRunner(proc processor.Processor, p *pool.Pool) *Runner {
	return &Runner{
		proc:    proc,
		pool:    p,
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
		log.Printf("file=%s open_error err=%v", name, err)
		return
	}

	var jobs sync.WaitGroup
	streamed := 0
	for rec := range stream {
		if rec.Err != nil {
			r.pool.Stats.ParseErr.Add(1)
			log.Printf("file=%s parse_err err=%v", name, rec.Err)
			continue
		}
		jobs.Add(1)
		_ = r.pool.Submit(pool.Job{
			File: name,
			User: rec.User,
			Ctx:  fctx,
			Done: jobs.Done,
		})
		streamed++
	}
	jobs.Wait()
	dur := time.Since(start)

	r.mu.Lock()
	r.files = append(r.files, FileStats{Path: path, Records: streamed, Duration: dur})
	r.mu.Unlock()

	log.Printf("file=%s records=%d duration=%s", name, streamed, dur)
}
