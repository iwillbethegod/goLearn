package user

import "sync"

type User struct {
	ID    string
	Name  string
	Email string
}

// Store is a concurrency-safe upsert store keyed by email.
// AddIfNew is the dedup gate: a user seen earlier (in any CSV)
// will not be processed again.
type Store struct {
	mu    sync.Mutex
	users map[string]User
}

func NewStore() *Store {
	return &Store{users: make(map[string]User)}
}

// AddIfNew returns true when the email had not been seen before.
// The check + insert happens under a single lock so two workers
// racing on the same email cannot both win.
func (s *Store) AddIfNew(u User) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.Email]; ok {
		return false
	}
	s.users[u.Email] = u
	return true
}

func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users)
}
