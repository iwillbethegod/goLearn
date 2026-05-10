package pool_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
)

func TestNewWithDefaults(t *testing.T) {
	p := pool.New(nil)
	if p.WorkerCount() != 0 {
		t.Fatalf("fresh pool workers = %d, want 0", p.WorkerCount())
	}
	if p.QueueLen() != 0 {
		t.Fatalf("fresh pool queue = %d, want 0", p.QueueLen())
	}
	p.Stop()
}

func TestStartSpawnsRequestedWorkers(t *testing.T) {
	p := pool.New(context.Background())
	p.Start(5)
	if got := p.WorkerCount(); got != 5 {
		t.Fatalf("workers = %d, want 5", got)
	}
	p.Stop()
}

func TestStartNegativeNoOp(t *testing.T) {
	p := pool.New(context.Background())
	p.Start(-3)
	if got := p.WorkerCount(); got != 0 {
		t.Fatalf("Start(-3) workers = %d, want 0", got)
	}
	p.Stop()
}

func TestAddRemoveWorker(t *testing.T) {
	p := pool.New(context.Background())
	p.AddWorker()
	p.AddWorker()
	if p.WorkerCount() != 2 {
		t.Fatalf("after 2 Adds = %d, want 2", p.WorkerCount())
	}
	id, err := p.RemoveWorker()
	if err != nil {
		t.Fatalf("RemoveWorker: %v", err)
	}
	if id == 0 {
		t.Fatalf("RemoveWorker id = 0")
	}
	if p.WorkerCount() != 1 {
		t.Fatalf("after Remove = %d, want 1", p.WorkerCount())
	}
	p.Stop()
}

func TestRemoveWorkerOnEmptyErrors(t *testing.T) {
	p := pool.New(context.Background())
	if _, err := p.RemoveWorker(); !errors.Is(err, pool.ErrNoWorkers) {
		t.Fatalf("RemoveWorker on empty = %v, want ErrNoWorkers", err)
	}
	p.Stop()
}

func TestSubmitRunsJobOnWorker(t *testing.T) {
	p := pool.New(context.Background())
	p.Start(2)
	defer p.Stop()

	var ran atomic.Bool
	done := make(chan struct{})
	if err := p.Submit(context.Background(), func(ctx context.Context) {
		if pool.WorkerID(ctx) == 0 {
			t.Errorf("WorkerID = 0; expected non-zero from Pool")
		}
		ran.Store(true)
		close(done)
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not execute")
	}
	if !ran.Load() {
		t.Fatal("ran flag false")
	}
}

func TestSubmitRespectsCallerCtxCancel(t *testing.T) {
	p := pool.New(context.Background())
	// 0 workers, queue full (size 0) → Submit must block until ctx cancels.
	pNoQueue := pool.New(context.Background(), pool.WithQueueSize(0))
	defer pNoQueue.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pNoQueue.Submit(ctx, func(_ context.Context) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit on cancelled ctx = %v, want context.Canceled", err)
	}
	p.Stop()
}

func TestSubmitAfterStopErrors(t *testing.T) {
	// Queue size 0 means `p.jobs <-` blocks once workers stop, so
	// select can only pick the closed-stop arm — deterministic.
	p := pool.New(context.Background(), pool.WithQueueSize(0))
	p.Start(1)
	p.Stop()
	if err := p.Submit(context.Background(), func(_ context.Context) {}); !errors.Is(err, pool.ErrStopped) {
		t.Fatalf("Submit after Stop = %v, want ErrStopped", err)
	}
}

func TestSubmitRespectsRootCtx(t *testing.T) {
	// As above, force jobs<- to block so the cancelled root-ctx arm
	// is the only ready select case.
	rootCtx, cancel := context.WithCancel(context.Background())
	p := pool.New(rootCtx, pool.WithQueueSize(0))
	cancel()
	defer p.Stop()
	if err := p.Submit(context.Background(), func(_ context.Context) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit after root cancel = %v, want context.Canceled", err)
	}
}

// Stop must drain queued jobs by invoking fn with a cancelled ctx so
// caller cleanup (WaitGroup.Done etc.) still fires.
func TestStopDrainsQueuedJobsWithCancelledCtx(t *testing.T) {
	p := pool.New(context.Background(), pool.WithQueueSize(4))
	// No workers — every Submit just enqueues.
	var fired atomic.Int32
	var sawCancelled atomic.Int32
	for i := 0; i < 4; i++ {
		if err := p.Submit(context.Background(), func(ctx context.Context) {
			fired.Add(1)
			if ctx.Err() != nil {
				sawCancelled.Add(1)
			}
		}); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	p.Stop()
	if fired.Load() != 4 || sawCancelled.Load() != 4 {
		t.Fatalf("fired=%d cancelled=%d, want 4/4", fired.Load(), sawCancelled.Load())
	}
}

func TestStopIsIdempotent(t *testing.T) {
	p := pool.New(context.Background())
	p.Start(2)
	p.Stop()
	p.Stop() // must not panic on the closed channel
}

func TestQueueLenReportsBuffered(t *testing.T) {
	p := pool.New(context.Background(), pool.WithQueueSize(8))
	defer p.Stop()
	// No workers — Submits sit in the queue.
	for i := 0; i < 3; i++ {
		_ = p.Submit(context.Background(), func(_ context.Context) {})
	}
	if got := p.QueueLen(); got != 3 {
		t.Fatalf("QueueLen = %d, want 3", got)
	}
}

func TestWorkerIDOnBareContextIsZero(t *testing.T) {
	if got := pool.WorkerID(context.Background()); got != 0 {
		t.Fatalf("WorkerID on bare ctx = %d, want 0", got)
	}
}

func TestWithWorkerIDRoundTrip(t *testing.T) {
	ctx := pool.WithWorkerID(context.Background(), 7)
	if got := pool.WorkerID(ctx); got != 7 {
		t.Fatalf("round-trip = %d, want 7", got)
	}
}

// Concurrent Submit + worker execution must be race-free under -race.
func TestPoolUnderLoadRaceFree(t *testing.T) {
	p := pool.New(context.Background(), pool.WithQueueSize(64))
	p.Start(8)
	defer p.Stop()

	const N = 1000
	var wg sync.WaitGroup
	wg.Add(N)
	var done atomic.Int32
	for i := 0; i < N; i++ {
		_ = p.Submit(context.Background(), func(_ context.Context) {
			done.Add(1)
			wg.Done()
		})
	}
	wg.Wait()
	if done.Load() != N {
		t.Fatalf("done = %d, want %d", done.Load(), N)
	}
}
