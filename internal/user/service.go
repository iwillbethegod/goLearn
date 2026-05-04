package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

// MinPasswordLen is the shortest password the Service will accept on
// registration. 8 is a defensible default for a learning project.
const MinPasswordLen = 8

const loginFailedMsg = "login failed"

type Service struct {
	repo    Repository
	logger  *slog.Logger
	metrics *metrics.Metrics
}

func NewService(repo Repository, logger *slog.Logger, metricsCollector *metrics.Metrics) *Service {
	return &Service{
		repo:    repo,
		logger:  logger,
		metrics: metricsCollector,
	}
}

func (s *Service) Logger() *slog.Logger {
	return s.logger
}

func (s *Service) AddUser(ctx context.Context, name, email string) (User, error) {
	trimmedName := strings.TrimSpace(name)
	trimmedEmail := strings.TrimSpace(email)
	if trimmedName == "" || trimmedEmail == "" {
		s.logger.Error("invalid add user request", "error", ErrInvalidUser)
		return User{}, ErrInvalidUser
	}

	if !IsValidEmail(trimmedEmail) {
		s.logger.Error("invalid email format", "error", ErrInvalidEmail, "email", trimmedEmail)
		return User{}, ErrInvalidEmail
	}

	newUser := User{
		ID:    s.generateID(),
		Name:  trimmedName,
		Email: trimmedEmail,
	}

	if err := s.repo.Add(newUser); err != nil {
		s.logger.Error("repository failed to add user", "error", err, "user_id", newUser.ID)
		return User{}, err
	}

	s.metrics.IncUserAdded()
	s.logger.Info("user created", "user_id", newUser.ID)
	return newUser, nil
}

func (s *Service) ListUsers() ([]User, error) {
	users, err := s.repo.List()
	if err != nil {
		s.logger.Error("repository failed to list users", "error", err)
		return nil, err
	}
	return users, nil
}

func (s *Service) RemoveUser(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		s.logger.Error("invalid remove user request", "error", ErrInvalidUser)
		return ErrInvalidUser
	}

	if err := s.repo.Remove(userID); err != nil {
		s.logger.Error("repository failed to remove user", "error", err, "user_id", userID)
		return err
	}

	s.logger.Info("user removed", "user_id", userID)
	return nil
}

func (s *Service) generateID() string {
	return fmt.Sprintf("u-%d", time.Now().UnixNano())
}

// Register creates a new user with a bcrypt-hashed password and
// persists them via the repository.
func (s *Service) Register(ctx context.Context, name, email, password string) (User, error) {
	trimmedName := strings.TrimSpace(name)
	trimmedEmail := strings.TrimSpace(email)
	if trimmedName == "" || trimmedEmail == "" {
		s.logger.Error("invalid register request", "error", ErrInvalidUser)
		return User{}, ErrInvalidUser
	}
	if !IsValidEmail(trimmedEmail) {
		s.logger.Error("invalid email format", "error", ErrInvalidEmail, "email", trimmedEmail)
		return User{}, ErrInvalidEmail
	}
	if len(password) < MinPasswordLen {
		s.logger.Error("password too short", "min", MinPasswordLen)
		return User{}, ErrInvalidPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.logger.Error("hash password failed", "error", err)
		return User{}, NewStorageError(err)
	}

	newUser := User{
		ID:           s.generateID(),
		Name:         trimmedName,
		Email:        trimmedEmail,
		PasswordHash: string(hash),
	}

	if err := s.repo.Add(newUser); err != nil {
		s.logger.Error("repository failed to add user", "error", err, "user_id", newUser.ID)
		return User{}, err
	}

	s.metrics.IncUserAdded()
	s.logger.Info("user registered", "user_id", newUser.ID, "email", newUser.Email)
	return newUser, nil
}

// Login looks up the user by email and verifies the password against
// the stored bcrypt hash. Wrong email and wrong password both return
// ErrInvalidCredential to avoid leaking which one was wrong.
func (s *Service) Login(ctx context.Context, email, password string) (User, error) {
	u, err := s.repo.GetByEmail(email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) || errors.Is(err, ErrInvalidEmail) {
			s.logger.Warn(loginFailedMsg, "reason", "no such user", "email", email)
			return User{}, ErrInvalidCredential
		}
		s.logger.Error("repository lookup failed", "error", err)
		return User{}, err
	}
	if u.PasswordHash == "" {
		s.logger.Warn(loginFailedMsg, "reason", "user has no password set", "email", email)
		return User{}, ErrInvalidCredential
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.logger.Warn(loginFailedMsg, "reason", "bad password", "email", email)
		return User{}, ErrInvalidCredential
	}
	s.logger.Info("login ok", "user_id", u.ID, "email", u.Email)
	return u, nil
}

// DeleteByEmail authenticates the email/password pair and then
// removes the user. Returns ErrInvalidCredential on any auth failure.
func (s *Service) DeleteByEmail(ctx context.Context, email, password string) error {
	u, err := s.Login(ctx, email, password)
	if err != nil {
		return err
	}
	if err := s.repo.Remove(u.ID); err != nil {
		s.logger.Error("repository failed to remove user", "error", err, "user_id", u.ID)
		return err
	}
	s.logger.Info("user deleted", "user_id", u.ID, "email", u.Email)
	return nil
}
