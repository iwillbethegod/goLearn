package user_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

// stubPublisher records every PublishUserCreated call. The optional
// errFn lets tests simulate broker failures.
type stubPublisher struct {
	calls atomic.Int32
	last  atomic.Pointer[model.User]
	errFn func() error
}

func (s *stubPublisher) PublishUserCreated(_ context.Context, u model.User) error {
	s.calls.Add(1)
	cp := u
	s.last.Store(&cp)
	if s.errFn != nil {
		return s.errFn()
	}
	return nil
}

func newSvc(pub user.Publisher) *user.Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := memory.NewUserRepo(logger)
	if pub == nil {
		return user.NewService(repo, logger, metrics.New())
	}
	return user.NewService(repo, logger, metrics.New(), user.WithPublisher(pub))
}

// TestRegisterPublishesAfterCommit asserts that a successful Register
// fans out exactly one user.created event after the repository write
// succeeds. Critical for the Day-6 "event-driven flow" deliverable.
func TestRegisterPublishesAfterCommit(t *testing.T) {
	pub := &stubPublisher{}
	svc := newSvc(pub)

	got, err := svc.Register(context.Background(), "Ada", "ada@example.com", "hunter22")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if pub.calls.Load() != 1 {
		t.Fatalf("publisher calls = %d, want 1", pub.calls.Load())
	}
	last := pub.last.Load()
	if last == nil || last.ID != got.ID || last.Email != "ada@example.com" {
		t.Fatalf("publisher saw %+v, want %+v", last, got)
	}
}

// TestRegisterDoesNotFailOnPublishError is the post-commit
// best-effort contract: the user is already in the DB by the time we
// publish, so a broker failure must not roll back or fail Register.
func TestRegisterDoesNotFailOnPublishError(t *testing.T) {
	pub := &stubPublisher{errFn: func() error { return errors.New("nats down") }}
	svc := newSvc(pub)

	u, err := svc.Register(context.Background(), "Ada", "ada@example.com", "hunter22")
	if err != nil {
		t.Fatalf("Register failed unexpectedly: %v", err)
	}
	if u.ID == "" {
		t.Fatalf("expected user to be created despite publish error")
	}
	// Repository should still hold the row.
	got, err := svc.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Email != "ada@example.com" {
		t.Fatalf("user not persisted: %+v", got)
	}
}

// TestRegisterNoPublisherDefault asserts that omitting WithPublisher
// uses the no-op default and Register still succeeds.
func TestRegisterNoPublisherDefault(t *testing.T) {
	svc := newSvc(nil) // no publisher option → no-op default
	if _, err := svc.Register(context.Background(), "Ada", "ada@example.com", "hunter22"); err != nil {
		t.Fatalf("Register without publisher: %v", err)
	}
}

// TestRegisterPublishSkippedOnRepoError makes sure we don't fan out
// events for users who failed to land in the DB (e.g. duplicate
// email).
func TestRegisterPublishSkippedOnRepoError(t *testing.T) {
	pub := &stubPublisher{}
	svc := newSvc(pub)

	if _, err := svc.Register(context.Background(), "Ada", "ada@example.com", "hunter22"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	// Second registration with same email → ErrDuplicateUser from repo.
	_, err := svc.Register(context.Background(), "Ada2", "ada@example.com", "hunter22")
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("expected ErrDuplicateUser, got %v", err)
	}
	if pub.calls.Load() != 1 {
		t.Fatalf("expected exactly 1 publish (after first Register), got %d", pub.calls.Load())
	}
}
