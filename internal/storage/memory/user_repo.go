package memory

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// UserRepo is an in-memory implementation of user.Repository
type UserRepo struct {
	mu     sync.RWMutex
	users  map[string]model.User
	emails map[string]string
	logger *slog.Logger
}

// NewUserRepo creates a new in-memory user repository
func NewUserRepo(logger *slog.Logger) *UserRepo {
	return &UserRepo{
		users:  make(map[string]model.User),
		emails: make(map[string]string),
		logger: logger,
	}
}

// Add adds a user to the in-memory store
func (r *UserRepo) Add(ctx context.Context, u model.User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	emailKey := strings.ToLower(strings.TrimSpace(u.Email))
	if emailKey == "" {
		return model.ErrInvalidUser
	}
	if _, found := r.emails[emailKey]; found {
		return model.ErrDuplicateUser
	}

	r.users[u.ID] = u
	r.emails[emailKey] = u.ID
	r.logger.Debug("user added to memory", "user_id", u.ID, "email", u.Email)
	return nil
}

// Get returns the user with the given ID, or ErrUserNotFound.
func (r *UserRepo) Get(ctx context.Context, id string) (model.User, error) {
	if err := ctx.Err(); err != nil {
		return model.User{}, err
	}
	if strings.TrimSpace(id) == "" {
		return model.User{}, model.ErrInvalidUser
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, found := r.users[id]
	if !found {
		return model.User{}, model.ErrUserNotFound
	}
	return u, nil
}

// Update overwrites the user record at u.ID. Detects email collision
// against another user before mutating.
func (r *UserRepo) Update(ctx context.Context, u model.User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	prev, found := r.users[u.ID]
	if !found {
		return model.ErrUserNotFound
	}
	newKey := strings.ToLower(strings.TrimSpace(u.Email))
	if newKey == "" {
		return model.ErrInvalidUser
	}
	prevKey := strings.ToLower(strings.TrimSpace(prev.Email))
	if newKey != prevKey {
		if _, taken := r.emails[newKey]; taken {
			return model.ErrDuplicateUser
		}
		delete(r.emails, prevKey)
		r.emails[newKey] = u.ID
	}
	if u.PasswordHash == "" {
		u.PasswordHash = prev.PasswordHash
	}
	r.users[u.ID] = u
	r.logger.Debug("user updated in memory", "user_id", u.ID)
	return nil
}

// GetByEmail returns the user with the given email (case-insensitive,
// whitespace-insensitive lookup).
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	if err := ctx.Err(); err != nil {
		return model.User{}, err
	}
	emailKey := strings.ToLower(strings.TrimSpace(email))
	if emailKey == "" {
		return model.User{}, model.ErrInvalidEmail
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, found := r.emails[emailKey]
	if !found {
		return model.User{}, model.ErrUserNotFound
	}
	return r.users[id], nil
}

// List returns all users from the in-memory store
func (r *UserRepo) List(ctx context.Context) ([]model.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]model.User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	return users, nil
}

// Remove removes a user by ID from the in-memory store
func (r *UserRepo) Remove(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	u, found := r.users[userID]
	if !found {
		return model.ErrUserNotFound
	}

	emailKey := strings.ToLower(strings.TrimSpace(u.Email))
	delete(r.users, userID)
	delete(r.emails, emailKey)
	r.logger.Debug("user removed from memory", "user_id", userID)
	return nil
}

var _ user.Repository = (*UserRepo)(nil)
