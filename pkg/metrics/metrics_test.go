package metrics_test

import (
	"sync"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func TestNewStartsAtZero(t *testing.T) {
	m := metrics.New()
	if got := m.UsersAdded(); got != 0 {
		t.Fatalf("UsersAdded on fresh = %d, want 0", got)
	}
}

func TestIncUserAddedIncrements(t *testing.T) {
	m := metrics.New()
	m.IncUserAdded()
	m.IncUserAdded()
	m.IncUserAdded()
	if got := m.UsersAdded(); got != 3 {
		t.Fatalf("UsersAdded after 3 incs = %d, want 3", got)
	}
}

// Concurrent IncUserAdded with the race detector enforces the atomic
// contract — a non-atomic counter would lose increments here.
func TestIncUserAddedConcurrent(t *testing.T) {
	const goroutines = 32
	const incsPer = 1000
	m := metrics.New()

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < incsPer; j++ {
				m.IncUserAdded()
			}
		}()
	}
	wg.Wait()

	want := int64(goroutines * incsPer)
	if got := m.UsersAdded(); got != want {
		t.Fatalf("UsersAdded after %d concurrent incs = %d, want %d", want, got, want)
	}
}

// UsersAdded must be safe to call alongside writers — atomic.Int64.Load.
func TestUsersAddedConcurrentReads(t *testing.T) {
	m := metrics.New()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				m.IncUserAdded()
			}
		}
	}()
	for i := 0; i < 1000; i++ {
		_ = m.UsersAdded()
	}
	close(stop)
}
