package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// checkCtx is the cooperative cancellation point used at the top of
// every repository call. The jsonfile backend is sync I/O, so ctx is
// only inspected pre-flight; once a write begins it runs to completion.
func checkCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

type UserRepo struct {
	mu     sync.RWMutex
	path   string
	logger *slog.Logger
	users  map[string]model.User
	emails map[string]string
}

func NewUserRepo(path string, logger *slog.Logger) (*UserRepo, error) {
	repo := &UserRepo{
		path:   path,
		logger: logger,
		users:  make(map[string]model.User),
		emails: make(map[string]string),
	}
	if err := repo.load(); err != nil {
		return nil, err
	}
	return repo, nil
}

func (r *UserRepo) Add(ctx context.Context, u model.User) error {
	if err := checkCtx(ctx); err != nil {
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
	if err := r.saveLocked(); err != nil {
		delete(r.users, u.ID)
		delete(r.emails, emailKey)
		return err
	}

	r.logger.Info("user persisted", "user_id", u.ID)
	return nil
}

func (r *UserRepo) Get(ctx context.Context, id string) (model.User, error) {
	if err := checkCtx(ctx); err != nil {
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

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	if err := checkCtx(ctx); err != nil {
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

// Update overwrites the user record at u.ID. Email re-indexing is
// transactional: an email change that collides with another user
// returns ErrDuplicateUser without mutating state.
func (r *UserRepo) Update(ctx context.Context, u model.User) error {
	if err := checkCtx(ctx); err != nil {
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
	}

	// Preserve fields the caller didn't intend to overwrite.
	if u.PasswordHash == "" {
		u.PasswordHash = prev.PasswordHash
	}
	r.users[u.ID] = u
	if newKey != prevKey {
		delete(r.emails, prevKey)
		r.emails[newKey] = u.ID
	}

	if err := r.saveLocked(); err != nil {
		// Roll back the in-memory mutation on persist failure.
		r.users[u.ID] = prev
		if newKey != prevKey {
			delete(r.emails, newKey)
			r.emails[prevKey] = u.ID
		}
		return err
	}
	r.logger.Info("user updated", "user_id", u.ID)
	return nil
}

func (r *UserRepo) List(ctx context.Context, limit, offset int) ([]model.User, int64, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, 0, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]model.User, 0, len(r.users))
	for _, existingUser := range r.users {
		users = append(users, existingUser)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})

	total := int64(len(users))
	if offset < 0 {
		offset = 0
	}
	if offset >= len(users) {
		return []model.User{}, total, nil
	}
	end := len(users)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	page := make([]model.User, end-offset)
	copy(page, users[offset:end])
	return page, total, nil
}

func (r *UserRepo) load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return model.NewStorageError(err)
	}
	if len(data) == 0 {
		return nil
	}

	var users []model.User
	if err := json.Unmarshal(data, &users); err != nil {
		return model.NewStorageError(err)
	}

	for _, existingUser := range users {
		emailKey := strings.ToLower(strings.TrimSpace(existingUser.Email))
		if existingUser.ID == "" || emailKey == "" {
			continue
		}
		r.users[existingUser.ID] = existingUser
		r.emails[emailKey] = existingUser.ID
	}
	return nil
}

func (r *UserRepo) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return model.NewStorageError(err)
	}

	users := make([]model.User, 0, len(r.users))
	for _, existingUser := range r.users {
		users = append(users, existingUser)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})

	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return model.NewStorageError(err)
	}
	data = append(data, '\n')

	tempPath := r.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return model.NewStorageError(err)
	}
	if err := os.Rename(tempPath, r.path); err != nil {
		return model.NewStorageError(err)
	}
	return nil
}

// Remove removes a user by ID from persistent JSON storage
func (r *UserRepo) Remove(ctx context.Context, userID string) error {
	if err := checkCtx(ctx); err != nil {
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

	if err := r.saveLocked(); err != nil {
		// Restore the user on save failure
		r.users[userID] = u
		r.emails[emailKey] = userID
		return err
	}

	r.logger.Info("user persisted after removal", "user_id", userID)
	return nil
}

var _ user.Repository = (*UserRepo)(nil)
