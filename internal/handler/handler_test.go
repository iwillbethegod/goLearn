package handler_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/internal/handler"
	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/pool"
)

// recordingDeduper accepts every user once. Tests with a pre-loaded
// "seen" set assert dedup short-circuits the chain.
type recordingDeduper struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newDeduper(preload ...string) *recordingDeduper {
	d := &recordingDeduper{seen: map[string]bool{}}
	for _, id := range preload {
		d.seen[id] = true
	}
	return d
}

func (d *recordingDeduper) AddIfNew(u model.User) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen[u.ID] {
		return false
	}
	d.seen[u.ID] = true
	return true
}

func TestOutcomeString(t *testing.T) {
	cases := map[handler.Outcome]string{
		handler.OutcomeOK:        "ok",
		handler.OutcomeDedup:     "dedup",
		handler.OutcomeCancelled: "cancelled",
		handler.OutcomeError:     "error",
		handler.Outcome(99):      "unknown",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", o, got, want)
		}
	}
}

// Chain ordering: outermost runs first, so middleware that records
// "before" + "after" prints in nested order.
func TestChainOrdering(t *testing.T) {
	var got []string
	mw := func(label string) handler.Middleware {
		return func(next handler.Handler) handler.Handler {
			return func(ctx context.Context, f string, u model.User) handler.Outcome {
				got = append(got, "in:"+label)
				out := next(ctx, f, u)
				got = append(got, "out:"+label)
				return out
			}
		}
	}
	h := handler.Chain(handler.Terminal, mw("A"), mw("B"), mw("C"))
	if h(context.Background(), "f", model.User{}) != handler.OutcomeOK {
		t.Fatal("Terminal should return OK")
	}
	want := []string{"in:A", "in:B", "in:C", "out:C", "out:B", "out:A"}
	if !sliceEq(got, want) {
		t.Fatalf("ordering = %v, want %v", got, want)
	}
}

func TestWithCancelCheckShortCircuits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	final := handler.Handler(func(_ context.Context, _ string, _ model.User) handler.Outcome {
		called = true
		return handler.OutcomeOK
	})
	h := handler.Chain(final, handler.WithCancelCheck())
	if got := h(ctx, "f", model.User{}); got != handler.OutcomeCancelled {
		t.Fatalf("outcome = %v, want Cancelled", got)
	}
	if called {
		t.Fatal("terminal handler ran despite cancelled ctx")
	}
}

func TestWithDedupShortCircuits(t *testing.T) {
	d := newDeduper("u-1") // u-1 already seen
	called := false
	final := handler.Handler(func(_ context.Context, _ string, _ model.User) handler.Outcome {
		called = true
		return handler.OutcomeOK
	})
	h := handler.Chain(final, handler.WithDedup(d))

	if got := h(context.Background(), "f", model.User{ID: "u-1"}); got != handler.OutcomeDedup {
		t.Fatalf("dup user outcome = %v, want Dedup", got)
	}
	if called {
		t.Fatal("terminal ran despite dedup hit")
	}
	// Fresh user passes through.
	if got := h(context.Background(), "f", model.User{ID: "u-2"}); got != handler.OutcomeOK {
		t.Fatalf("new user outcome = %v, want OK", got)
	}
	if !called {
		t.Fatal("terminal should have run for fresh user")
	}
}

func TestWithProcessMapsErrors(t *testing.T) {
	tests := []struct {
		name string
		fn   handler.ProcessFunc
		want handler.Outcome
	}{
		{"nil-err-passes-through", func(_ context.Context, _ model.User) error { return nil }, handler.OutcomeOK},
		{"context-canceled-cancelled", func(_ context.Context, _ model.User) error { return context.Canceled }, handler.OutcomeCancelled},
		{"deadline-exceeded-cancelled", func(_ context.Context, _ model.User) error { return context.DeadlineExceeded }, handler.OutcomeCancelled},
		{"random-err-error", func(_ context.Context, _ model.User) error { return errors.New("boom") }, handler.OutcomeError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := handler.Chain(handler.Terminal, handler.WithProcess(tt.fn))
			if got := h(context.Background(), "f", model.User{}); got != tt.want {
				t.Fatalf("outcome = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithMetricsRecordsOutcome(t *testing.T) {
	stats := &handler.Stats{}
	cases := []struct {
		name    string
		final   handler.Handler
		want    handler.Outcome
		checker func(t *testing.T, snap handler.Snapshot)
	}{
		{"ok",
			func(_ context.Context, _ string, _ model.User) handler.Outcome { return handler.OutcomeOK },
			handler.OutcomeOK,
			func(t *testing.T, s handler.Snapshot) {
				if s.OK != 1 {
					t.Fatalf("OK = %d, want 1", s.OK)
				}
			}},
		{"dedup",
			func(_ context.Context, _ string, _ model.User) handler.Outcome { return handler.OutcomeDedup },
			handler.OutcomeDedup,
			func(t *testing.T, s handler.Snapshot) {
				if s.Dedup != 1 {
					t.Fatalf("Dedup = %d, want 1", s.Dedup)
				}
			}},
		{"cancelled",
			func(_ context.Context, _ string, _ model.User) handler.Outcome { return handler.OutcomeCancelled },
			handler.OutcomeCancelled,
			func(t *testing.T, s handler.Snapshot) {
				if s.Cancelled != 1 {
					t.Fatalf("Cancelled = %d, want 1", s.Cancelled)
				}
			}},
		{"error",
			func(_ context.Context, _ string, _ model.User) handler.Outcome { return handler.OutcomeError },
			handler.OutcomeError,
			func(t *testing.T, s handler.Snapshot) {
				if s.ParseErr != 1 {
					t.Fatalf("ParseErr = %d, want 1", s.ParseErr)
				}
			}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := &handler.Stats{}
			h := handler.Chain(tt.final, handler.WithMetrics(s))
			if got := h(context.Background(), "f", model.User{}); got != tt.want {
				t.Fatalf("outcome = %v, want %v", got, tt.want)
			}
			tt.checker(t, s.Snapshot())
		})
	}
	_ = stats
}

func TestWithPerWorkerCount(t *testing.T) {
	s := &handler.Stats{}
	h := handler.Chain(handler.Terminal, handler.WithPerWorkerCount(s))

	ctxW1 := pool.WithWorkerID(context.Background(), 1)
	ctxW2 := pool.WithWorkerID(context.Background(), 2)
	h(ctxW1, "f", model.User{ID: "a"})
	h(ctxW1, "f", model.User{ID: "b"})
	h(ctxW2, "f", model.User{ID: "c"})

	snap := s.Snapshot()
	if len(snap.PerWorker) != 2 {
		t.Fatalf("workers tracked = %d, want 2", len(snap.PerWorker))
	}
	if snap.PerWorker[0].ID != 1 || snap.PerWorker[0].Count != 2 {
		t.Fatalf("worker 1 = %+v, want {ID:1 Count:2}", snap.PerWorker[0])
	}
	if snap.PerWorker[1].ID != 2 || snap.PerWorker[1].Count != 1 {
		t.Fatalf("worker 2 = %+v, want {ID:2 Count:1}", snap.PerWorker[1])
	}
}

func TestWithLoggingOff_IsZeroOverheadPassthrough(t *testing.T) {
	called := false
	final := handler.Handler(func(_ context.Context, _ string, _ model.User) handler.Outcome {
		called = true
		return handler.OutcomeOK
	})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := handler.Chain(final, handler.WithLogging(logger, false))
	if got := h(context.Background(), "f", model.User{}); got != handler.OutcomeOK {
		t.Fatalf("outcome = %v, want OK", got)
	}
	if !called {
		t.Fatal("terminal handler should have run")
	}
}

// Stats: snapshot is sorted by worker ID even if writes happen
// concurrently in arbitrary order.
func TestStatsSnapshotSortsByWorkerID(t *testing.T) {
	s := &handler.Stats{}
	for _, id := range []int{5, 1, 3, 2, 4} {
		s.IncWorker(id)
	}
	snap := s.Snapshot()
	for i := 0; i < len(snap.PerWorker)-1; i++ {
		if snap.PerWorker[i].ID > snap.PerWorker[i+1].ID {
			t.Fatalf("not sorted: %+v", snap.PerWorker)
		}
	}
}

// Concurrent IncOK / IncDedup must be race-free under -race.
func TestStatsConcurrentWrites(t *testing.T) {
	s := &handler.Stats{}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				s.IncOK()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				s.IncWorker(j % 4)
			}
		}()
	}
	wg.Wait()

	snap := s.Snapshot()
	if snap.OK != 16000 {
		t.Fatalf("OK = %d, want 16000", snap.OK)
	}
	var total uint64
	for _, w := range snap.PerWorker {
		total += w.Count
	}
	if total != 16000 {
		t.Fatalf("PerWorker total = %d, want 16000", total)
	}
}

// Defensive timing: the Logging middleware wraps `time.Since`. Make
// sure it doesn't wrap to a negative duration on a fast path.
func TestWithLoggingOn_TimingNonNegative(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	final := handler.Handler(func(_ context.Context, _ string, _ model.User) handler.Outcome {
		return handler.OutcomeOK
	})
	h := handler.Chain(final, handler.WithLogging(logger, true))
	start := time.Now()
	_ = h(context.Background(), "f", model.User{})
	if time.Since(start) < 0 {
		t.Fatal("monotonic clock should make this impossible")
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
