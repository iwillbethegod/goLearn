package user

import (
	"context"
	"errors"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/pkg/logger"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func TestAddUser(t *testing.T) {
	repo := NewInMemoryUserRepository()
	svc := NewService(repo, logger.NewLogger(), metrics.New())

	user, err := svc.AddUser(context.Background(), "Alice", "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID == "" {
		t.Fatal("expected generated user ID")
	}
	if user.Email != "alice@example.com" {
		t.Fatalf("expected email alice@example.com, got %s", user.Email)
	}
}

func TestAddDuplicateUser(t *testing.T) {
	repo := NewInMemoryUserRepository()
	svc := NewService(repo, logger.NewLogger(), metrics.New())

	_, err := svc.AddUser(context.Background(), "Alice", "alice@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.AddUser(context.Background(), "Alice", "alice@example.com")
	if !errors.Is(err, ErrDuplicateUser) {
		t.Fatalf("expected ErrDuplicateUser, got %v", err)
	}
}

func TestAddInvalidUser(t *testing.T) {
	repo := NewInMemoryUserRepository()
	svc := NewService(repo, logger.NewLogger(), metrics.New())

	_, err := svc.AddUser(context.Background(), "", "alice@example.com")
	if !errors.Is(err, ErrInvalidUser) {
		t.Fatalf("expected ErrInvalidUser, got %v", err)
	}
}
