package memory_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
)

func newRepo(t *testing.T) *memory.UserRepo {
	t.Helper()
	return memory.NewUserRepo(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestAdd_HappyPath(t *testing.T) {
	r := newRepo(t)
	u := model.User{ID: "u-1", Name: "Ada", Email: "ada@x.com"}
	if err := r.Add(context.Background(), u); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := r.Get(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "ada@x.com" {
		t.Fatalf("got %+v", got)
	}
}

func TestAdd_DuplicateEmailRejected(t *testing.T) {
	r := newRepo(t)
	_ = r.Add(context.Background(), model.User{ID: "u-1", Email: "x@y.com"})
	err := r.Add(context.Background(), model.User{ID: "u-2", Email: "x@y.com"})
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("got %v, want ErrDuplicateUser", err)
	}
}

func TestAdd_DuplicateEmailIsCaseInsensitive(t *testing.T) {
	r := newRepo(t)
	_ = r.Add(context.Background(), model.User{ID: "u-1", Email: "x@y.com"})
	err := r.Add(context.Background(), model.User{ID: "u-2", Email: "X@Y.COM"})
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("got %v, want ErrDuplicateUser (case-insensitive)", err)
	}
}

func TestAdd_EmptyEmailRejected(t *testing.T) {
	r := newRepo(t)
	err := r.Add(context.Background(), model.User{ID: "u-1", Email: "  "})
	if !errors.Is(err, model.ErrInvalidUser) {
		t.Fatalf("got %v, want ErrInvalidUser", err)
	}
}

func TestGet_NotFound(t *testing.T) {
	r := newRepo(t)
	_, err := r.Get(context.Background(), "nope")
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("got %v, want ErrUserNotFound", err)
	}
}

func TestGet_BlankIDRejected(t *testing.T) {
	r := newRepo(t)
	if _, err := r.Get(context.Background(), "  "); !errors.Is(err, model.ErrInvalidUser) {
		t.Fatalf("blank id should be ErrInvalidUser, got %v", err)
	}
}

func TestGetByEmail_CaseInsensitive(t *testing.T) {
	r := newRepo(t)
	_ = r.Add(context.Background(), model.User{ID: "u-1", Email: "Mixed@Case.com"})
	got, err := r.GetByEmail(context.Background(), "MIXED@case.COM")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got.ID != "u-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestGetByEmail_BlankRejected(t *testing.T) {
	r := newRepo(t)
	if _, err := r.GetByEmail(context.Background(), ""); !errors.Is(err, model.ErrInvalidEmail) {
		t.Fatalf("got %v, want ErrInvalidEmail", err)
	}
}

func TestUpdate_HappyPath(t *testing.T) {
	r := newRepo(t)
	_ = r.Add(context.Background(), model.User{ID: "u-1", Name: "old", Email: "x@y.com", PasswordHash: "ORIG"})
	if err := r.Update(context.Background(), model.User{ID: "u-1", Name: "new", Email: "x@y.com"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := r.Get(context.Background(), "u-1")
	if got.Name != "new" {
		t.Fatalf("Name not updated: %+v", got)
	}
	// Empty PasswordHash on update should preserve the stored one.
	if got.PasswordHash != "ORIG" {
		t.Fatalf("PasswordHash overwritten: %q", got.PasswordHash)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	r := newRepo(t)
	err := r.Update(context.Background(), model.User{ID: "missing", Email: "x@y.com"})
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("got %v, want ErrUserNotFound", err)
	}
}

func TestUpdate_EmailCollision(t *testing.T) {
	r := newRepo(t)
	_ = r.Add(context.Background(), model.User{ID: "u-1", Email: "a@x.com"})
	_ = r.Add(context.Background(), model.User{ID: "u-2", Email: "b@x.com"})
	err := r.Update(context.Background(), model.User{ID: "u-1", Email: "b@x.com"})
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("got %v, want ErrDuplicateUser", err)
	}
}

func TestUpdate_EmailReindexed(t *testing.T) {
	r := newRepo(t)
	_ = r.Add(context.Background(), model.User{ID: "u-1", Email: "a@x.com"})
	if err := r.Update(context.Background(), model.User{ID: "u-1", Email: "new@x.com"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// Old email lookup should now miss.
	if _, err := r.GetByEmail(context.Background(), "a@x.com"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("old email still resolves: %v", err)
	}
	if _, err := r.GetByEmail(context.Background(), "new@x.com"); err != nil {
		t.Fatalf("new email lookup: %v", err)
	}
}

func TestRemove(t *testing.T) {
	r := newRepo(t)
	_ = r.Add(context.Background(), model.User{ID: "u-1", Email: "a@x.com"})
	if err := r.Remove(context.Background(), "u-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := r.Get(context.Background(), "u-1"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("get after remove: %v", err)
	}
	// Email index also cleared.
	if _, err := r.GetByEmail(context.Background(), "a@x.com"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("getByEmail after remove: %v", err)
	}
}

func TestRemove_NotFound(t *testing.T) {
	r := newRepo(t)
	err := r.Remove(context.Background(), "missing")
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("got %v, want ErrUserNotFound", err)
	}
}

func TestList_PaginationAndSorting(t *testing.T) {
	r := newRepo(t)
	for _, id := range []string{"u-3", "u-1", "u-2"} {
		_ = r.Add(context.Background(), model.User{ID: id, Email: id + "@x.com"})
	}
	users, total, err := r.List(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	for i, want := range []string{"u-1", "u-2", "u-3"} {
		if users[i].ID != want {
			t.Errorf("users[%d] = %q, want %q (sorted by ID)", i, users[i].ID, want)
		}
	}

	page, _, _ := r.List(context.Background(), 2, 1)
	if len(page) != 2 || page[0].ID != "u-2" || page[1].ID != "u-3" {
		t.Fatalf("paged page = %+v", page)
	}

	empty, _, _ := r.List(context.Background(), 10, 100)
	if len(empty) != 0 {
		t.Fatalf("offset past end = %+v", empty)
	}
}

func TestClose_NoOp(t *testing.T) {
	r := newRepo(t)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestCtxCancel_PropagatesAcrossOps(t *testing.T) {
	r := newRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, op := range []func() error{
		func() error { return r.Add(ctx, model.User{ID: "x", Email: "x@x"}) },
		func() error { _, err := r.Get(ctx, "x"); return err },
		func() error { _, err := r.GetByEmail(ctx, "x"); return err },
		func() error { return r.Update(ctx, model.User{ID: "x", Email: "x@x"}) },
		func() error { return r.Remove(ctx, "x") },
		func() error { _, _, err := r.List(ctx, 0, 0); return err },
	} {
		if err := op(); !errors.Is(err, context.Canceled) {
			t.Errorf("op returned %v, want context.Canceled", err)
		}
	}
}

// Concurrent Add of distinct emails is race-free under -race.
func TestConcurrentAdds(t *testing.T) {
	r := newRepo(t)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := model.User{
				ID:    fmt.Sprintf("u-%d", i),
				Email: fmt.Sprintf("user%d@x.com", i),
			}
			_ = r.Add(context.Background(), u)
		}()
	}
	wg.Wait()
	_, total, _ := r.List(context.Background(), 0, 0)
	if total != 64 {
		t.Fatalf("after 64 adds total = %d, want 64", total)
	}
}
