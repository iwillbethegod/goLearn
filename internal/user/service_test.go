package user_test

import (
	"context"
	"errors"
	"fmt"
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

func TestRegister_RejectsShortPassword(t *testing.T) {
	svc := newSvc(nil)
	_, err := svc.Register(context.Background(), "Ada", "ada@x.com", "abc")
	if !errors.Is(err, model.ErrInvalidPassword) {
		t.Fatalf("got %v, want ErrInvalidPassword", err)
	}
}

func TestRegister_RejectsBadEmail(t *testing.T) {
	svc := newSvc(nil)
	_, err := svc.Register(context.Background(), "Ada", "not-an-email", "longenough")
	if !errors.Is(err, model.ErrInvalidEmail) {
		t.Fatalf("got %v, want ErrInvalidEmail", err)
	}
}

func TestRegister_RejectsBlankNameOrEmail(t *testing.T) {
	svc := newSvc(nil)
	if _, err := svc.Register(context.Background(), "  ", "a@x.com", "longenough"); !errors.Is(err, model.ErrInvalidUser) {
		t.Errorf("blank name = %v, want ErrInvalidUser", err)
	}
	if _, err := svc.Register(context.Background(), "Ada", "  ", "longenough"); !errors.Is(err, model.ErrInvalidUser) {
		t.Errorf("blank email = %v, want ErrInvalidUser", err)
	}
}

func TestAddUser_HappyPath(t *testing.T) {
	svc := newSvc(nil)
	got, err := svc.AddUser(context.Background(), "Ada", "ada@x.com")
	if err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if got.ID == "" || got.Email != "ada@x.com" {
		t.Fatalf("got %+v", got)
	}
}

func TestAddUser_BadEmail(t *testing.T) {
	svc := newSvc(nil)
	_, err := svc.AddUser(context.Background(), "Ada", "not-an-email")
	if !errors.Is(err, model.ErrInvalidEmail) {
		t.Fatalf("got %v, want ErrInvalidEmail", err)
	}
}

func TestAddUser_BlankFields(t *testing.T) {
	svc := newSvc(nil)
	if _, err := svc.AddUser(context.Background(), "  ", "a@x.com"); !errors.Is(err, model.ErrInvalidUser) {
		t.Errorf("blank name: %v", err)
	}
}

func TestGetUser_HappyAndNotFound(t *testing.T) {
	svc := newSvc(nil)
	added, _ := svc.AddUser(context.Background(), "Ada", "ada@x.com")

	got, err := svc.GetUser(context.Background(), added.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != added.ID {
		t.Fatalf("got %+v", got)
	}
	if _, err := svc.GetUser(context.Background(), "missing"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("missing = %v, want ErrUserNotFound", err)
	}
	if _, err := svc.GetUser(context.Background(), "  "); !errors.Is(err, model.ErrInvalidUser) {
		t.Fatalf("blank id = %v, want ErrInvalidUser", err)
	}
}

func TestRemoveUser_HappyAndNotFound(t *testing.T) {
	svc := newSvc(nil)
	added, _ := svc.AddUser(context.Background(), "Ada", "ada@x.com")
	if err := svc.RemoveUser(context.Background(), added.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := svc.RemoveUser(context.Background(), "missing"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("missing = %v, want ErrUserNotFound", err)
	}
	if err := svc.RemoveUser(context.Background(), "  "); !errors.Is(err, model.ErrInvalidUser) {
		t.Fatalf("blank id = %v, want ErrInvalidUser", err)
	}
}

func TestUpdateUser_HappyAndValidation(t *testing.T) {
	svc := newSvc(nil)
	added, _ := svc.AddUser(context.Background(), "Ada", "ada@x.com")

	updated, err := svc.UpdateUser(context.Background(), added.ID, "Ada Lovelace", "")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Ada Lovelace" {
		t.Fatalf("name = %q", updated.Name)
	}
	// Bad email surfaces as ErrInvalidEmail
	if _, err := svc.UpdateUser(context.Background(), added.ID, "", "not-an-email"); !errors.Is(err, model.ErrInvalidEmail) {
		t.Fatalf("bad email = %v, want ErrInvalidEmail", err)
	}
	// Missing user
	if _, err := svc.UpdateUser(context.Background(), "missing", "X", ""); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("missing = %v, want ErrUserNotFound", err)
	}
}

func TestListUsers(t *testing.T) {
	svc := newSvc(nil)
	for i := 0; i < 3; i++ {
		_, _ = svc.Register(context.Background(), fmt.Sprintf("u%d", i), fmt.Sprintf("u%d@x.com", i), "longenough")
	}
	users, total, err := svc.ListUsers(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if total != 3 || len(users) != 3 {
		t.Fatalf("total=%d len=%d", total, len(users))
	}
}

func TestLogger_NotNil(t *testing.T) {
	svc := newSvc(nil)
	if svc.Logger() == nil {
		t.Fatal("Logger() returned nil")
	}
}

func TestLogin_HappyPath(t *testing.T) {
	svc := newSvc(nil)
	if _, err := svc.Register(context.Background(), "Ada", "ada@x.com", "hunter22"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Login(context.Background(), "ada@x.com", "hunter22")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if got.Email != "ada@x.com" {
		t.Fatalf("got %+v", got)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	svc := newSvc(nil)
	_, _ = svc.Register(context.Background(), "Ada", "ada@x.com", "hunter22")
	_, err := svc.Login(context.Background(), "ada@x.com", "wrong-password")
	if !errors.Is(err, model.ErrInvalidCredential) {
		t.Fatalf("got %v, want ErrInvalidCredential", err)
	}
}

func TestLogin_NoSuchUser_ReturnsSameSentinel(t *testing.T) {
	svc := newSvc(nil)
	_, err := svc.Login(context.Background(), "ghost@x.com", "any")
	if !errors.Is(err, model.ErrInvalidCredential) {
		t.Fatalf("got %v, want ErrInvalidCredential (no leak which factor was wrong)", err)
	}
}

func TestDeleteByEmail_HappyPath(t *testing.T) {
	svc := newSvc(nil)
	u, _ := svc.Register(context.Background(), "Ada", "ada@x.com", "hunter22")
	if err := svc.DeleteByEmail(context.Background(), "ada@x.com", "hunter22"); err != nil {
		t.Fatalf("DeleteByEmail: %v", err)
	}
	if _, err := svc.GetUser(context.Background(), u.ID); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("user still present after delete: %v", err)
	}
}

func TestDeleteByEmail_WrongPasswordMasksResult(t *testing.T) {
	svc := newSvc(nil)
	_, _ = svc.Register(context.Background(), "Ada", "ada@x.com", "hunter22")
	err := svc.DeleteByEmail(context.Background(), "ada@x.com", "wrong-password")
	if !errors.Is(err, model.ErrInvalidCredential) {
		t.Fatalf("got %v, want ErrInvalidCredential", err)
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
