package tokens

import "sync"

// Store holds one Bucket per user, lazily created on first access
// using the same Config for every new bucket. The store itself is
// concurrency-safe; once a Bucket is returned, it owns its own
// mutex and survives concurrent callers.
type Store struct {
	mu      sync.Mutex
	buckets map[string]*Bucket
	cfg     Config
}

// NewStore creates an empty store. Buckets are populated on demand
// via ForUser.
func NewStore(cfg Config) *Store {
	return &Store{
		buckets: make(map[string]*Bucket),
		cfg:     cfg,
	}
}

// ForUser returns the bucket for userID, creating one with the
// store's config defaults if it doesn't exist yet. The returned
// Bucket is safe for concurrent use; the store's mutex is only
// held during the lookup-or-create.
func (s *Store) ForUser(userID string) *Bucket {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.buckets[userID]; ok {
		return b
	}
	b := NewBucket(s.cfg.Capacity, s.cfg.RatePerSecond())
	s.buckets[userID] = b
	return b
}

// Config returns the per-bucket config the store creates new
// buckets from. Useful for surfacing in /healthz or admin
// endpoints.
func (s *Store) Config() Config { return s.cfg }
