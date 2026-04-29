package memory

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// UserRepo is an in-memory implementation of user.Repository
type UserRepo struct {
	mu     sync.RWMutex
	users  map[string]user.User
	emails map[string]string
	logger *slog.Logger
}

// NewUserRepo creates a new in-memory user repository
func NewUserRepo(logger *slog.Logger) *UserRepo {
	return &UserRepo{
		users:  make(map[string]user.User),
		emails: make(map[string]string),
		logger: logger,
	}
}

// Add adds a user to the in-memory store
func (r *UserRepo) Add(u user.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	emailKey := strings.ToLower(strings.TrimSpace(u.Email))
	if emailKey == "" {
		return user.ErrInvalidUser
	}
	if _, found := r.emails[emailKey]; found {
		return user.ErrDuplicateUser
	}

	r.users[u.ID] = u
	r.emails[emailKey] = u.ID
	r.logger.Debug("user added to memory", "user_id", u.ID, "email", u.Email)
	return nil
}

// List returns all users from the in-memory store
func (r *UserRepo) List() ([]user.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]user.User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	return users, nil
}

var _ user.Repository = (*UserRepo)(nil)
