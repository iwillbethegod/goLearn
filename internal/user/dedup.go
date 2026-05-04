package user

import (
	"strings"
	"sync"
)

// DedupStore is a concurrency-safe transient set of user records keyed
// by normalised email. It is the dedup gate used during a single
// ingest run — distinct from Repository, which is the persistent
// credential store. AddIfNew is atomic: two workers racing on the
// same email cannot both win.
//
// Emails are normalised (lower-case + trimmed) so "Alice@Example.com"
// and "alice@example.com " hash to the same key.
type DedupStore struct {
	mu    sync.Mutex
	users map[string]User
}

func NewDedupStore() *DedupStore {
	return &DedupStore{users: make(map[string]User)}
}

// AddIfNew returns true when the email had not been seen before.
func (s *DedupStore) AddIfNew(u User) bool {
	key := normaliseEmail(u.Email)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[key]; ok {
		return false
	}
	s.users[key] = u
	return true
}

// Count returns the number of distinct users seen.
func (s *DedupStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users)
}

func normaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
