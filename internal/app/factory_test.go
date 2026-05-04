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

func TestAddInvalidEmail(t *testing.T) {
	log := logger.NewLogger()
	repo, err := NewRepository(RepositoryConfig{
		Type:   TypeMemory,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	svc := user.NewService(repo, log, metrics.New())

	// Test invalid email format
	_, err = svc.AddUser(context.Background(), "Alice", "invalid-email")
	if !errors.Is(err, user.ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail for 'invalid-email', got %v", err)
	}

	// Test missing @ symbol
	_, err = svc.AddUser(context.Background(), "Bob", "bob.example.com")
	if !errors.Is(err, user.ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail for 'bob.example.com', got %v", err)
	}

	// Test valid email should work
	u, err := svc.AddUser(context.Background(), "Charlie", "charlie@example.com")
	if err != nil {
		t.Fatalf("expected valid email to work, got error: %v", err)
	}
	if u.Email != "charlie@example.com" {
		t.Fatalf("expected charlie@example.com, got %s", u.Email)
	}
}

func TestRemoveUser(t *testing.T) {
	log := logger.NewLogger()
	repo, err := NewRepository(RepositoryConfig{
		Type:   TypeMemory,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	svc := user.NewService(repo, log, metrics.New())

	// Add a user
	u, err := svc.AddUser(context.Background(), "Diana", "diana@example.com")
	if err != nil {
		t.Fatalf("failed to add user: %v", err)
	}

	// Remove the user
	err = svc.RemoveUser(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("failed to remove user: %v", err)
	}

	// Verify user is gone
	users, err := svc.ListUsers()
	if err != nil {
		t.Fatalf("failed to list users: %v", err)
	}
	for _, user := range users {
		if user.ID == u.ID {
			t.Fatal("user should have been removed but still exists")
		}
	}
}

func TestRemoveNonexistentUser(t *testing.T) {
	log := logger.NewLogger()
	repo, err := NewRepository(RepositoryConfig{
		Type:   TypeMemory,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	svc := user.NewService(repo, log, metrics.New())

	// Try to remove non-existent user
	err = svc.RemoveUser(context.Background(), "u-nonexistent")
	if !errors.Is(err, user.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}
