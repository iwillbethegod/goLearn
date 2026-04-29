package user

import (
	"strings"
	"sync"
)

type inMemoryRepository struct {
	mu     sync.RWMutex
	users  map[string]User
	emails map[string]string
}

func NewInMemoryUserRepository() Repository {
	return &inMemoryRepository{
		users:  make(map[string]User),
		emails: make(map[string]string),
	}
}

func (r *inMemoryRepository) Add(u User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	emailKey := strings.ToLower(strings.TrimSpace(u.Email))
	if emailKey == "" {
		return ErrInvalidUser
	}
	if _, found := r.emails[emailKey]; found {
		return ErrDuplicateUser
	}

	r.users[u.ID] = u
	r.emails[emailKey] = u.ID
	return nil
}

func (r *inMemoryRepository) List() ([]User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	return users, nil
}
