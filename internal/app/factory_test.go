package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func TestAddUser(t *testing.T) {
	log := newTestLogger()
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
	log := newTestLogger()
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
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("expected ErrDuplicateUser, got %v", err)
	}
}

func TestAddInvalidUser(t *testing.T) {
	log := newTestLogger()
	repo, err := NewRepository(RepositoryConfig{
		Type:   TypeMemory,
		Logger: log,
	})
	if err != nil {
		t.Fatalf("failed to create repository: %v", err)
	}
	svc := user.NewService(repo, log, metrics.New())

	_, err = svc.AddUser(context.Background(), "", "alice@example.com")
	if !errors.Is(err, model.ErrInvalidUser) {
		t.Fatalf("expected ErrInvalidUser, got %v", err)
	}
}

func TestAddInvalidEmail(t *testing.T) {
	log := newTestLogger()
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
	if !errors.Is(err, model.ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail for 'invalid-email', got %v", err)
	}

	// Test missing @ symbol
	_, err = svc.AddUser(context.Background(), "Bob", "bob.example.com")
	if !errors.Is(err, model.ErrInvalidEmail) {
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
	log := newTestLogger()
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
	users, err := svc.ListUsers(context.Background())
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
	log := newTestLogger()
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
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

// --- Day 3: Get / Update -----------------------------------------------------

func newSvc(t *testing.T) *user.Service {
	t.Helper()
	log := newTestLogger()
	repo, err := NewRepository(RepositoryConfig{Type: TypeMemory, Logger: log})
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	return user.NewService(repo, log, metrics.New())
}

func TestGetUser(t *testing.T) {
	svc := newSvc(t)
	u, err := svc.AddUser(context.Background(), "Eve", "eve@example.com")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := svc.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != u.ID || got.Email != u.Email {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, u)
	}
}

func TestGetMissingUser(t *testing.T) {
	svc := newSvc(t)
	_, err := svc.GetUser(context.Background(), "u-nonexistent")
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestUpdateUserName(t *testing.T) {
	svc := newSvc(t)
	u, _ := svc.AddUser(context.Background(), "Frank", "frank@example.com")
	got, err := svc.UpdateUser(context.Background(), u.ID, "Frank Renamed", "")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Name != "Frank Renamed" {
		t.Fatalf("name not updated: %q", got.Name)
	}
	if got.Email != u.Email {
		t.Fatalf("email should be unchanged: %q", got.Email)
	}
}

func TestUpdateUserEmailCollision(t *testing.T) {
	svc := newSvc(t)
	a, _ := svc.AddUser(context.Background(), "A", "a@example.com")
	_, _ = svc.AddUser(context.Background(), "B", "b@example.com")
	_, err := svc.UpdateUser(context.Background(), a.ID, "", "b@example.com")
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("expected ErrDuplicateUser, got %v", err)
	}
}

func TestUpdateUserInvalidEmail(t *testing.T) {
	svc := newSvc(t)
	u, _ := svc.AddUser(context.Background(), "G", "g@example.com")
	_, err := svc.UpdateUser(context.Background(), u.ID, "", "not-an-email")
	if !errors.Is(err, model.ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}
}

func TestUpdateMissingUser(t *testing.T) {
	svc := newSvc(t)
	_, err := svc.UpdateUser(context.Background(), "u-nope", "X", "x@example.com")
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
