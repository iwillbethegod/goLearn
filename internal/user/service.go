package user

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

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
