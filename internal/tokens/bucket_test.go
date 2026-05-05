package tokens

import (
	"sync"
	"testing"
)

func TestBucket_StartsFull(t *testing.T) {
	b := NewBucket(100, 10)
	if got := b.Available(); got != 100 {
		t.Fatalf("available=%d want 100", got)
	}
}

func TestBucket_TakeWithinCapacity(t *testing.T) {
	b := NewBucket(100, 0) // no refill
	ok, rem := b.Take(40)
	if !ok || rem != 60 {
		t.Fatalf("Take(40)=(%v,%d), want (true,60)", ok, rem)
	}
}

func TestBucket_TakeRejectedWhenInsufficient(t *testing.T) {
	b := NewBucket(100, 0)
	_, _ = b.Take(80)
	ok, rem := b.Take(50)
	if ok || rem != 20 {
		t.Fatalf("Take(50) on 20=(%v,%d), want (false,20)", ok, rem)
	}
}

func TestBucket_ReturnCappedAtCapacity(t *testing.T) {
	b := NewBucket(100, 0)
	_, _ = b.Take(50) // tokens: 50
	if rem := b.Return(30); rem != 80 {
		t.Fatalf("after Return(30) on 50: %d want 80", rem)
	}
	// Returning more than was taken should not exceed capacity.
	if rem := b.Return(500); rem != 100 {
		t.Fatalf("overflow Return: %d want capped at 100", rem)
	}
}

func TestBucket_ConcurrentTakeIsAtomic(t *testing.T) {
	// 100 goroutines compete for a bucket that can grant at most 20.
	// If Take were not atomic, more than 20 could win.
	const (
		capacity   int64 = 1_000
		each       int64 = 50
		goroutines       = 100
	)
	expected := capacity / each // 20

	b := NewBucket(capacity, 0)
	var wg sync.WaitGroup
	var grants int64
	var grantMu sync.Mutex

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := b.Take(each); ok {
				grantMu.Lock()
				grants++
				grantMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if grants != expected {
		t.Fatalf("grants=%d want %d (no double-spending)", grants, expected)
	}
	if got := b.Available(); got != 0 {
		t.Fatalf("after exhausting: available=%d want 0", got)
	}
}

func TestStore_LazyCreatePerUser(t *testing.T) {
	s := NewStore(Config{Capacity: 100, RatePerMin: 60})
	a := s.ForUser("alice")
	b := s.ForUser("bob")
	if a == b {
		t.Fatal("expected separate buckets per user")
	}
	if a.Capacity() != 100 || b.Capacity() != 100 {
		t.Fatalf("expected capacity=100, got a=%d b=%d", a.Capacity(), b.Capacity())
	}
	a2 := s.ForUser("alice")
	if a != a2 {
		t.Fatal("repeated ForUser must return the same bucket")
	}
}

func TestConfig_RatePerSecond(t *testing.T) {
	c := Config{Capacity: 20000, RatePerMin: 10000}
	if got := c.RatePerSecond(); got != 10000.0/60.0 {
		t.Fatalf("rate/sec=%v", got)
	}
}

func TestConfig_Validate(t *testing.T) {
	if err := (Config{Capacity: 0, RatePerMin: 1}).Validate(); err == nil {
		t.Fatal("expected error for capacity=0")
	}
	if err := (Config{Capacity: 1, RatePerMin: -1}).Validate(); err == nil {
		t.Fatal("expected error for rate=-1")
	}
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
}
