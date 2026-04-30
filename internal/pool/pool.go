package pool

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// Job is one CSV row to be processed. Ctx is the *file's* context,
// so cancelling one file marks every still-queued job from that file
// as a no-op without affecting jobs from other files.
type Job struct {
	File string
	User user.User
	Ctx  context.Context
	// Done is invoked exactly once when the job leaves the pool —
	// whether processed, deduped, or skipped due to cancellation.
	// The submitter uses this to track per-file completion.
	Done func()
}

// Pool is a fixed-size worker pool fed by a single jobs channel.
type Pool struct {
	workers int
	jobs    chan Job
	store   *user.Store
	wg      sync.WaitGroup
	// simulated per-job work; overridable from tests.
	work time.Duration
}

func New(workers int, queue int, store *user.Store) *Pool {
	return &Pool{
		workers: workers,
		jobs:    make(chan Job, queue),
		store:   store,
		work:    2 * time.Millisecond,
	}
}

// Start launches workers. ctx is the root context — when it is
// cancelled, every worker drains and exits.
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.run(ctx, i)
	}
}

func (p *Pool) Submit(j Job) { p.jobs <- j }

// Stop closes the input channel and waits for workers to drain.
func (p *Pool) Stop() {
	close(p.jobs)
	p.wg.Wait()
}

func (p *Pool) run(ctx context.Context, id int) {
	defer p.wg.Done()
	for j := range p.jobs {
		p.process(ctx, id, j)
	}
}

func (p *Pool) process(ctx context.Context, id int, j Job) {
	defer j.Done()
	// Root cancelled -> bail entirely.
	if ctx.Err() != nil {
		return
	}
	// File cancelled -> skip this job but keep the worker alive
	// for jobs from other (still-active) files.
	if j.Ctx.Err() != nil {
		log.Printf("[worker %d] cancelled file=%s email=%s", id, j.File, j.User.Email)
		return
	}
	if !p.store.AddIfNew(j.User) {
		log.Printf("[worker %d] dedup    file=%s email=%s", id, j.File, j.User.Email)
		return
	}
	// Simulated I/O — interruptible.
	select {
	case <-time.After(p.work):
	case <-j.Ctx.Done():
		log.Printf("[worker %d] cancelled file=%s email=%s (mid-work)", id, j.File, j.User.Email)
		return
	case <-ctx.Done():
		return
	}
	log.Printf("[worker %d] upsert   file=%s email=%s", id, j.File, j.User.Email)
}
