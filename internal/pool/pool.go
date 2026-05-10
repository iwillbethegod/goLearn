// Package pool implements a domain-agnostic worker pool with runtime
// AddWorker/RemoveWorker and per-job context propagation. The pool
// has no knowledge of records, dedup, or business logic — callers
// submit a function and a context, and a worker invokes the function
// with a context carrying its worker ID (see WorkerID).
package pool

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrNoWorkers = errors.New("pool: no workers to remove")
	ErrStopped   = errors.New("pool: stopped")
)

// Job is the function executed by a worker. The context passed in
// is the caller's context decorated with the handling worker's ID.
type Job func(ctx context.Context)

type queuedJob struct {
	ctx context.Context
	fn  Job
}

type worker struct {
	id   int
	quit chan struct{}
}

// Pool is a dynamically-resizable worker pool. Workers exit on quit
// (RemoveWorker), stop (Stop), or root context cancellation.
type Pool struct {
	mu      sync.Mutex
	workers []*worker
	nextID  int
	jobs    chan queuedJob
	stop    chan struct{}
	rootCtx context.Context
	wg      sync.WaitGroup
}

// Option configures a Pool at construction time.
type Option func(*Pool)

// WithQueueSize sets the buffered jobs-channel capacity (default 64).
func WithQueueSize(n int) Option {
	return func(p *Pool) {
		if n < 0 {
			n = 0
		}
		p.jobs = make(chan queuedJob, n)
	}
}

// New constructs a Pool. Start must be called separately to spawn
// the initial workers; this lets tests build a Pool with zero workers
// and add them by hand.
func New(rootCtx context.Context, opts ...Option) *Pool {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	p := &Pool{
		rootCtx: rootCtx,
		stop:    make(chan struct{}),
		jobs:    make(chan queuedJob, 64),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Start spawns initialN workers.
func (p *Pool) Start(initialN int) {
	if initialN < 0 {
		initialN = 0
	}
	for i := 0; i < initialN; i++ {
		p.AddWorker()
	}
}

// AddWorker spawns one worker and returns its ID.
func (p *Pool) AddWorker() int {
	p.mu.Lock()
	p.nextID++
	w := &worker{id: p.nextID, quit: make(chan struct{})}
	p.workers = append(p.workers, w)
	p.wg.Add(1)
	p.mu.Unlock()
	go p.run(w)
	return w.id
}

// RemoveWorker pops the most-recently-added worker and closes its quit
// channel. The worker finishes its current job, if any, before exiting.
func (p *Pool) RemoveWorker() (int, error) {
	p.mu.Lock()
	if len(p.workers) == 0 {
		p.mu.Unlock()
		return 0, ErrNoWorkers
	}
	last := len(p.workers) - 1
	w := p.workers[last]
	p.workers = p.workers[:last]
	p.mu.Unlock()
	close(w.quit)
	return w.id, nil
}

// WorkerCount returns the current number of live workers.
func (p *Pool) WorkerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

// QueueLen returns the number of buffered jobs.
func (p *Pool) QueueLen() int { return len(p.jobs) }

// Submit blocks until the job is enqueued, ctx is done, the root
// context is cancelled, or the pool is stopped. The caller's fn must
// release any per-job resources (e.g. WaitGroup.Done) — when Submit
// returns an error, fn is *not* invoked, so the caller must clean up.
func (p *Pool) Submit(ctx context.Context, fn Job) error {
	if ctx == nil {
		ctx = p.rootCtx
	}
	select {
	case <-p.stop:
		return ErrStopped
	case <-p.rootCtx.Done():
		return p.rootCtx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case p.jobs <- queuedJob{ctx, fn}:
		return nil
	}
}

// Stop signals every worker to exit, waits for them, then drains any
// jobs left in the queue (invoking each with a cancelled context so
// the caller's WaitGroup.Done in fn still fires). Idempotent.
//
// Contract: callers must ensure no goroutine is mid-Submit when Stop
// is called, otherwise a job can leak between Submit's send and
// Stop's drain.
func (p *Pool) Stop() {
	p.mu.Lock()
	select {
	case <-p.stop:
		p.mu.Unlock()
		return
	default:
		close(p.stop)
	}
	p.mu.Unlock()
	p.wg.Wait()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for {
		select {
		case qj := <-p.jobs:
			qj.fn(cancelled)
		default:
			return
		}
	}
}

func (p *Pool) run(w *worker) {
	defer p.wg.Done()
	for {
		select {
		case <-w.quit:
			return
		case <-p.stop:
			return
		case <-p.rootCtx.Done():
			return
		case qj := <-p.jobs:
			qj.fn(WithWorkerID(qj.ctx, w.id))
		}
	}
}
