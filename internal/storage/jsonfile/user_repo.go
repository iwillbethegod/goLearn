package jsonfile

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

type UserRepo struct {
	mu     sync.RWMutex
	path   string
	logger *slog.Logger
	users  map[string]user.User
	emails map[string]string
}

func NewUserRepo(path string, logger *slog.Logger) (*UserRepo, error) {
	repo := &UserRepo{
		path:   path,
		logger: logger,
		users:  make(map[string]user.User),
		emails: make(map[string]string),
	}
	if err := repo.load(); err != nil {
		return nil, err
	}
	return repo, nil
}

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
	if err := r.saveLocked(); err != nil {
		delete(r.users, u.ID)
		delete(r.emails, emailKey)
		return err
	}

	r.logger.Info("user persisted", "user_id", u.ID)
	return nil
}

func (r *UserRepo) List() ([]user.User, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	users := make([]user.User, 0, len(r.users))
	for _, existingUser := range r.users {
		users = append(users, existingUser)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})
	return users, nil
}

func (r *UserRepo) load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return user.NewStorageError(err)
	}
	if len(data) == 0 {
		return nil
	}

	var users []user.User
	if err := json.Unmarshal(data, &users); err != nil {
		return user.NewStorageError(err)
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
		return user.NewStorageError(err)
	}

	users := make([]user.User, 0, len(r.users))
	for _, existingUser := range r.users {
		users = append(users, existingUser)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})

	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return user.NewStorageError(err)
	}
	data = append(data, '\n')

	tempPath := r.path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return user.NewStorageError(err)
	}
	if err := os.Rename(tempPath, r.path); err != nil {
		return user.NewStorageError(err)
	}
	return nil
}

var _ user.Repository = (*UserRepo)(nil)
