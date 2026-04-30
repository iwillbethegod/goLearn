// Package pool implements a worker pool with runtime add/remove,
// per-file context cancellation, atomic stats, and an injected
// per-record handler so the pool itself stays format-agnostic.
package pool

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

var (
	ErrNoWorkers = errors.New("pool: no workers to remove")
	ErrStopped   = errors.New("pool: stopped")
)

// ProcessFunc is the per-record handler. It must respect ctx and return
// ctx.Err() if cancelled mid-flight, otherwise nil on success.
type ProcessFunc func(ctx context.Context, u user.User) error

// MakeMockProcessRow returns a ProcessFunc that sleeps for a uniform
// random duration in [minD, maxD], cancellable via ctx. Without the
// cancel-aware select, per-file cancellation would lag by up to one
// full sleep per in-flight record.
func MakeMockProcessRow(minD, maxD time.Duration) ProcessFunc {
	if minD < 0 {
		minD = 0
	}
	if maxD <= minD {
		maxD = minD + time.Microsecond
	}
	span := int64(maxD - minD)
	return func(ctx context.Context, _ user.User) error {
		d := minD + time.Duration(rand.Int64N(span))
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// MockProcessRow is the default 10–500ms-per-record handler.
var MockProcessRow = MakeMockProcessRow(10*time.Millisecond, 500*time.Millisecond)

// Job is one record's worth of work. Ctx is the per-file context — when
// it is cancelled, this job becomes a no-op without affecting jobs from
// other files. Done is invoked exactly once after handling.
type Job struct {
	File string
	User user.User
	Ctx  context.Context
	Done func()
}

// Stats aggregates pool-wide counters using atomics so the REPL can read
// them concurrently without locks.
type Stats struct {
	OK        atomic.Uint64
	Dedup     atomic.Uint64
	Cancelled atomic.Uint64
	ParseErr  atomic.Uint64
	perWorker sync.Map // map[int]*atomic.Uint64
}

func (s *Stats) incWorker(id int) {
	if v, ok := s.perWorker.Load(id); ok {
		v.(*atomic.Uint64).Add(1)
		return
	}
	n := new(atomic.Uint64)
	actual, loaded := s.perWorker.LoadOrStore(id, n)
	if loaded {
		n = actual.(*atomic.Uint64)
	}
	n.Add(1)
}

// PerWorker returns a snapshot of per-worker job counts, sorted by id.
func (s *Stats) PerWorker() []WorkerCount {
	var out []WorkerCount
	s.perWorker.Range(func(k, v any) bool {
		out = append(out, WorkerCount{ID: k.(int), Count: v.(*atomic.Uint64).Load()})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type WorkerCount struct {
	ID    int
	Count uint64
}

type worker struct {
	id   int
	quit chan struct{}
}

// Pool is a dynamically-resizable worker pool fed by a single buffered
// jobs channel. Workers exit on quit (RemoveWorker), stop (Stop), or
// rootCtx cancellation.
type Pool struct {
	mu      sync.Mutex
	workers []*worker
	nextID  int
	jobs    chan Job
	stop    chan struct{}
	rootCtx context.Context
	store   *user.Store
	process ProcessFunc
	wg      sync.WaitGroup
	Stats   Stats
}

func New(rootCtx context.Context, queueSize int, store *user.Store, process ProcessFunc) *Pool {
	if process == nil {
		process = MockProcessRow
	}
	return &Pool{
		jobs:    make(chan Job, queueSize),
		stop:    make(chan struct{}),
		rootCtx: rootCtx,
		store:   store,
		process: process,
	}
}

func (p *Pool) Start(initialN int) {
	for i := 0; i < initialN; i++ {
		p.AddWorker()
	}
}

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
// channel. The worker finishes its current job (if any) before exiting,
// so removal is graceful — no half-done records.
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

func (p *Pool) WorkerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

func (p *Pool) QueueLen() int { return len(p.jobs) }

// Submit blocks until the job is enqueued, the job context is done,
// the root context is cancelled, or the pool is stopped. On any non-nil
// return, j.Done is invoked by Submit so the caller's WaitGroup releases.
func (p *Pool) Submit(j Job) error {
	select {
	case <-p.stop:
		safeDone(j)
		return ErrStopped
	case <-p.rootCtx.Done():
		safeDone(j)
		return p.rootCtx.Err()
	case <-j.Ctx.Done():
		safeDone(j)
		return j.Ctx.Err()
	case p.jobs <- j:
		return nil
	}
}

func safeDone(j Job) {
	if j.Done != nil {
		j.Done()
	}
}

// Stop signals every worker to exit, waits for them, then drains any
// jobs left in the queue (calling Done on each). Idempotent.
//
// We deliberately do not close p.jobs: Submit is a select that races
// the send against p.stop, and closing the channel under a racing
// sender would panic. Workers exit on p.stop, then Stop drains.
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
	for {
		select {
		case j := <-p.jobs:
			p.Stats.Cancelled.Add(1)
			safeDone(j)
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
		case j := <-p.jobs:
			p.handle(w.id, j)
		}
	}
}

// handle is the per-job pipeline: cancellation check → dedup → process.
// Dedup runs *here* rather than in Submit so we don't commit a user to
// the Store for a job that may never run (e.g. the file is cancelled
// while it sits in the queue).
func (p *Pool) handle(id int, j Job) {
	defer j.Done()
	p.Stats.incWorker(id)

	if j.Ctx.Err() != nil {
		p.Stats.Cancelled.Add(1)
		log.Printf("worker=%d file=%s id=%s result=cancelled", id, j.File, j.User.ID)
		return
	}
	if !p.store.AddIfNew(j.User) {
		p.Stats.Dedup.Add(1)
		log.Printf("worker=%d file=%s id=%s result=dedup", id, j.File, j.User.ID)
		return
	}
	start := time.Now()
	if err := p.process(j.Ctx, j.User); err != nil {
		p.Stats.Cancelled.Add(1)
		log.Printf("worker=%d file=%s id=%s result=cancelled dur=%s", id, j.File, j.User.ID, time.Since(start))
		return
	}
	p.Stats.OK.Add(1)
	log.Printf("worker=%d file=%s id=%s result=ok dur=%s", id, j.File, j.User.ID, time.Since(start))
}
