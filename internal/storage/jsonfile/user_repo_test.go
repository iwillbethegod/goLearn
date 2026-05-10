package jsonfile_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/jsonfile"
)

func newRepo(t *testing.T) (*jsonfile.UserRepo, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo, err := jsonfile.NewUserRepo(path, logger)
	if err != nil {
		t.Fatalf("NewUserRepo: %v", err)
	}
	return repo, path
}

func TestNewUserRepo_LoadsExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	seed := []model.User{{ID: "u-1", Name: "Ada", Email: "a@x.com", PasswordHash: "h"}}
	body, _ := json.Marshal(seed)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo, err := jsonfile.NewUserRepo(path, logger)
	if err != nil {
		t.Fatalf("NewUserRepo: %v", err)
	}
	got, err := repo.Get(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "a@x.com" {
		t.Fatalf("loaded user wrong: %+v", got)
	}
}

func TestNewUserRepo_MissingFileIsEmpty(t *testing.T) {
	repo, _ := newRepo(t)
	_, total, err := repo.List(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
}

func TestNewUserRepo_CorruptFileSurfaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := jsonfile.NewUserRepo(path, logger); err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
}

func TestAdd_PersistsToDisk(t *testing.T) {
	repo, path := newRepo(t)
	u := model.User{ID: "u-1", Name: "Ada", Email: "ada@x.com", PasswordHash: "h"}
	if err := repo.Add(context.Background(), u); err != nil {
		t.Fatalf("Add: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got []model.User
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].ID != "u-1" {
		t.Fatalf("disk has %+v", got)
	}
}

func TestAdd_DuplicateEmailRejected(t *testing.T) {
	repo, _ := newRepo(t)
	_ = repo.Add(context.Background(), model.User{ID: "u-1", Email: "x@y.com"})
	err := repo.Add(context.Background(), model.User{ID: "u-2", Email: "x@y.com"})
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("got %v, want ErrDuplicateUser", err)
	}
}

func TestUpdate_PreservesPasswordWhenEmpty(t *testing.T) {
	repo, _ := newRepo(t)
	_ = repo.Add(context.Background(), model.User{ID: "u-1", Name: "old", Email: "x@y.com", PasswordHash: "ORIG"})
	if err := repo.Update(context.Background(), model.User{ID: "u-1", Name: "new", Email: "x@y.com"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := repo.Get(context.Background(), "u-1")
	if got.Name != "new" {
		t.Fatalf("Name not updated: %+v", got)
	}
	if got.PasswordHash != "ORIG" {
		t.Fatalf("PasswordHash overwritten: %q", got.PasswordHash)
	}
}

func TestUpdate_EmailReindexedOnDisk(t *testing.T) {
	repo, _ := newRepo(t)
	_ = repo.Add(context.Background(), model.User{ID: "u-1", Email: "old@x.com"})
	if err := repo.Update(context.Background(), model.User{ID: "u-1", Email: "new@x.com"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := repo.GetByEmail(context.Background(), "old@x.com"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("old email lookup: %v", err)
	}
	got, err := repo.GetByEmail(context.Background(), "new@x.com")
	if err != nil {
		t.Fatalf("new email lookup: %v", err)
	}
	if got.ID != "u-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestUpdate_EmailCollision(t *testing.T) {
	repo, _ := newRepo(t)
	_ = repo.Add(context.Background(), model.User{ID: "u-1", Email: "a@x.com"})
	_ = repo.Add(context.Background(), model.User{ID: "u-2", Email: "b@x.com"})
	err := repo.Update(context.Background(), model.User{ID: "u-1", Email: "b@x.com"})
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("got %v, want ErrDuplicateUser", err)
	}
}

func TestRemove(t *testing.T) {
	repo, _ := newRepo(t)
	_ = repo.Add(context.Background(), model.User{ID: "u-1", Email: "a@x.com"})
	if err := repo.Remove(context.Background(), "u-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := repo.Get(context.Background(), "u-1"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("get after remove: %v", err)
	}
	if _, err := repo.GetByEmail(context.Background(), "a@x.com"); !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("getByEmail after remove: %v", err)
	}
}

func TestRemove_NotFound(t *testing.T) {
	repo, _ := newRepo(t)
	err := repo.Remove(context.Background(), "missing")
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("got %v, want ErrUserNotFound", err)
	}
}

func TestList_PaginationAndSorting(t *testing.T) {
	repo, _ := newRepo(t)
	for _, id := range []string{"u-3", "u-1", "u-2"} {
		_ = repo.Add(context.Background(), model.User{ID: id, Email: id + "@x.com"})
	}
	users, total, err := repo.List(context.Background(), 2, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(users) != 2 || users[0].ID != "u-2" {
		t.Fatalf("paged list = %+v", users)
	}
}

func TestClose_NoOp(t *testing.T) {
	repo, _ := newRepo(t)
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	r1, err := jsonfile.NewUserRepo(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := r1.Add(context.Background(), model.User{ID: "u-1", Email: "a@x.com", PasswordHash: "h"}); err != nil {
		t.Fatal(err)
	}

	r2, err := jsonfile.NewUserRepo(path, logger)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := r2.Get(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Email != "a@x.com" {
		t.Fatalf("got %+v", got)
	}
}
