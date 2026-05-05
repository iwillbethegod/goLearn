// Package tokens implements a per-user token-bucket rate limiter
// used by the day-4 gRPC contract between cmd/api (server) and
// cmd/ingest (client).
//
// Design choice: lazy refill. Every Take/Return/Available call
// recomputes the current balance from the elapsed time since the
// last refill. There's no background goroutine, no ticker, no
// allocation per refill — just a mutex per bucket and arithmetic.
package tokens

import (
	"sync"
	"time"
)

// Bucket is a single user's token bucket. Safe for concurrent use.
type Bucket struct {
	mu           sync.Mutex
	capacity     int64
	tokens       int64
	refillPerSec float64
	lastRefill   time.Time
}

// NewBucket starts the bucket full at the given capacity, refilling
// at refillPerSec tokens per second. capacity is clamped to >= 1
// and refillPerSec to >= 0.
func NewBucket(capacity int64, refillPerSec float64) *Bucket {
	if capacity < 1 {
		capacity = 1
	}
	if refillPerSec < 0 {
		refillPerSec = 0
	}
	return &Bucket{
		capacity:     capacity,
		tokens:       capacity,
		refillPerSec: refillPerSec,
		lastRefill:   time.Now(),
	}
}

// Take atomically reserves n tokens. Returns granted=false (with
// remaining = current available count) if the bucket has fewer than
// n tokens after refill. n must be > 0.
func (b *Bucket) Take(n int64) (granted bool, remaining int64) {
	if n <= 0 {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.refillLocked()
		return true, b.tokens
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	if b.tokens < n {
		return false, b.tokens
	}
	b.tokens -= n
	return true, b.tokens
}

// Return credits n tokens back to the bucket, capped at capacity.
// Used when a CSV processing run failed partway and some rows
// never got processed.
func (b *Bucket) Return(n int64) (remaining int64) {
	if n < 0 {
		n = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	b.tokens += n
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	return b.tokens
}

// Available returns the current token count after applying any
// time-based refill. Read-only.
func (b *Bucket) Available() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	return b.tokens
}

// Capacity returns the bucket's maximum token count.
func (b *Bucket) Capacity() int64 {
	return b.capacity
}

// refillLocked tops up the bucket based on elapsed time since the
// last refill. lastRefill advances by the time the granted tokens
// represent (not "now"), so fractional tokens accrue correctly
// across many short intervals.
func (b *Bucket) refillLocked() {
	if b.refillPerSec == 0 || b.tokens >= b.capacity {
		// Even with no refill, advance lastRefill so a later
		// refillPerSec change starts the clock fresh.
		b.lastRefill = time.Now()
		return
	}
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	add := int64(elapsed * b.refillPerSec)
	if add <= 0 {
		return
	}
	b.tokens += add
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	// Advance only by the time those tokens actually represent —
	// the rest is fractional and stays "owed" until the next call.
	consumedSecs := float64(add) / b.refillPerSec
	b.lastRefill = b.lastRefill.Add(time.Duration(consumedSecs * float64(time.Second)))
}
