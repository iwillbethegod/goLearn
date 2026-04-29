package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/logger"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func TestAddUser(t *testing.T) {
	log := logger.NewLogger()
	repo, err := NewRepository(RepositoryConfig{
		Type:   TypeMemory,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	svc := user.NewService(repo, log, metrics.New())

	u, err := svc.AddUser(context.Background(), "Alice", "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.ID == "" {
		t.Fatal("expected generated user ID")
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("expected email alice@example.com, got %s", u.Email)
	}
}

func TestAddDuplicateUser(t *testing.T) {
	log := logger.NewLogger()
	repo, err := NewRepository(RepositoryConfig{
		Type:   TypeMemory,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	svc := user.NewService(repo, log, metrics.New())

	_, err = svc.AddUser(context.Background(), "Alice", "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.AddUser(context.Background(), "Alice", "alice@example.com")
	if !errors.Is(err, user.ErrDuplicateUser) {
		t.Fatalf("expected ErrDuplicateUser, got %v", err)
	}
}

func TestAddInvalidUser(t *testing.T) {
	log := logger.NewLogger()
	repo, err := NewRepository(RepositoryConfig{
		Type:   TypeMemory,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	svc := user.NewService(repo, log, metrics.New())

	_, err = svc.AddUser(context.Background(), "", "alice@example.com")
	if !errors.Is(err, user.ErrInvalidUser) {
		t.Fatalf("expected ErrInvalidUser, got %v", err)
	}
}
