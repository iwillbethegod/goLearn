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

func (s *Service) generateID() string {
	return fmt.Sprintf("u-%d", time.Now().UnixNano())
}
