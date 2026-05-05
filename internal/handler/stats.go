package handler

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Stats holds atomic counters written by middleware. The atomic types
// stay private so callers cannot leak them; reads go through Snapshot.
type Stats struct {
	ok        atomic.Uint64
	dedup     atomic.Uint64
	cancelled atomic.Uint64
	parseErr  atomic.Uint64
	perWorker sync.Map // map[int]*atomic.Uint64
}

func (s *Stats) IncOK()        { s.ok.Add(1) }
func (s *Stats) IncDedup()     { s.dedup.Add(1) }
func (s *Stats) IncCancelled() { s.cancelled.Add(1) }
func (s *Stats) IncParseErr()  { s.parseErr.Add(1) }

func (s *Stats) IncWorker(id int) {
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

// Snapshot is a value-typed snapshot of Stats at a moment in time.
type Snapshot struct {
	OK        uint64
	Dedup     uint64
	Cancelled uint64
	ParseErr  uint64
	PerWorker []WorkerCount
}

type WorkerCount struct {
	ID    int
	Count uint64
}

func (s *Stats) Snapshot() Snapshot {
	snap := Snapshot{
		OK:        s.ok.Load(),
		Dedup:     s.dedup.Load(),
		Cancelled: s.cancelled.Load(),
		ParseErr:  s.parseErr.Load(),
	}
	s.perWorker.Range(func(k, v any) bool {
		snap.PerWorker = append(snap.PerWorker, WorkerCount{
			ID:    k.(int),
			Count: v.(*atomic.Uint64).Load(),
		})
		return true
	})
	sort.Slice(snap.PerWorker, func(i, j int) bool {
		return snap.PerWorker[i].ID < snap.PerWorker[j].ID
	})
	return snap
}
