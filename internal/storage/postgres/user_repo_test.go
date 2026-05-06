package postgres_test

// Day-5 integration tests. Each test spins up a fresh disposable
// Postgres container via testcontainers-go, runs the project's
// migrations against it, and exercises the Repository implementation
// end-to-end. Skipped automatically (-short) so `make test` stays
// fast; CI runs the full suite via `make test-race`.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres"
)

// migrationsURL points at the project's golang-migrate sources from
// inside this test file (file:// requires an absolute path on macOS).
func migrationsURL(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../../db/migrations")
	if err != nil {
		t.Fatalf("abs migrations path: %v", err)
	}
	return "file://" + abs
}

func newTestRepo(t *testing.T) *postgres.UserRepo {
	t.Helper()
	if testing.Short() {
		t.Skip("postgres integration test (requires Docker) — skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() {
		// Use a fresh ctx — the test ctx might already be cancelled.
		_ = ctr.Terminate(context.Background())
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	if err := postgres.Migrate(migrationsURL(t), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo, err := postgres.NewUserRepo(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("NewUserRepo: %v", err)
	}
	t.Cleanup(repo.Close)
	return repo
}

// auditCount runs a raw COUNT against registration_log so the
// transactional-Add tests can assert on the audit side-effect.
func auditCount(t *testing.T, dsn string) int {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("audit pool: %v", err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM registration_log").Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

// TestAdd_PersistsAndAuditsAtomically asserts that a successful Add
// lands BOTH the users row and the registration_log row inside a
// single transaction.
func TestAdd_PersistsAndAuditsAtomically(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	u := model.User{ID: "u-001", Name: "Alice", Email: "alice@example.com", PasswordHash: "$bcrypt$"}
	if err := repo.Add(ctx, u); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// users row landed.
	got, err := repo.Get(ctx, "u-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Email != "alice@example.com" || got.Name != "Alice" {
		t.Fatalf("Get returned wrong row: %+v", got)
	}

	// registration_log row landed (transaction committed).
	users, _ := repo.List(ctx)
	if len(users) != 1 {
		t.Fatalf("List len=%d, want 1", len(users))
	}
}

// TestAdd_DuplicateEmailRollsBackBoth asserts the case-insensitive
// unique index rejects the second insert AND that the failed
// transaction does not leak a registration_log entry.
func TestAdd_DuplicateEmailRollsBackBoth(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	a := model.User{ID: "u-a", Name: "Alice", Email: "alice@example.com"}
	if err := repo.Add(ctx, a); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	// Same email but different casing must still be a duplicate.
	b := model.User{ID: "u-b", Name: "Alice 2", Email: "ALICE@EXAMPLE.com"}
	err := repo.Add(ctx, b)
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("expected ErrDuplicateUser, got %v", err)
	}

	// users still has only one row.
	users, _ := repo.List(ctx)
	if len(users) != 1 {
		t.Fatalf("after rejected Add: len=%d, want 1", len(users))
	}
	// the failed transaction did NOT leave an audit row behind.
	got, _ := repo.Get(ctx, "u-b")
	if got.ID != "" {
		t.Fatalf("u-b should not exist: %+v", got)
	}
}

func TestGet_NotFoundMapsToErrUserNotFound(t *testing.T) {
	repo := newTestRepo(t)
	_, err := repo.Get(context.Background(), "u-nonexistent")
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestGetByEmail_CaseInsensitiveLookup(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := repo.Add(ctx, model.User{ID: "u-1", Name: "A", Email: "Mixed@Case.com"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByEmail(ctx, "mixed@case.COM")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got.ID != "u-1" {
		t.Fatalf("wrong row: %+v", got)
	}
}

func TestUpdate_PreservesPasswordHashWhenEmpty(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	if err := repo.Add(ctx, model.User{
		ID: "u-1", Name: "A", Email: "a@x.com", PasswordHash: "ORIGINAL",
	}); err != nil {
		t.Fatal(err)
	}
	// Update with empty PasswordHash; existing hash must be preserved.
	if err := repo.Update(ctx, model.User{
		ID: "u-1", Name: "A renamed", Email: "a@x.com", PasswordHash: "",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := repo.Get(ctx, "u-1")
	if got.Name != "A renamed" {
		t.Fatalf("name not updated: %q", got.Name)
	}
	if got.PasswordHash != "ORIGINAL" {
		t.Fatalf("password hash overwritten: %q", got.PasswordHash)
	}
}

func TestUpdate_EmailCollisionMapped(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	_ = repo.Add(ctx, model.User{ID: "u-a", Name: "A", Email: "a@x.com"})
	_ = repo.Add(ctx, model.User{ID: "u-b", Name: "B", Email: "b@x.com"})

	err := repo.Update(ctx, model.User{ID: "u-a", Name: "A", Email: "b@x.com"})
	if !errors.Is(err, model.ErrDuplicateUser) {
		t.Fatalf("expected ErrDuplicateUser, got %v", err)
	}
}

func TestRemove_NotFoundMapped(t *testing.T) {
	repo := newTestRepo(t)
	err := repo.Remove(context.Background(), "u-nope")
	if !errors.Is(err, model.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestRemove_OK_DeletesAuditTooViaCascade(t *testing.T) {
	// dial directly so we can audit the cascade.
	if testing.Short() {
		t.Skip("postgres integration — skipped in -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	defer func() { _ = ctr.Terminate(context.Background()) }()

	dsn, _ := ctr.ConnectionString(ctx, "sslmode=disable")
	if err := postgres.Migrate(migrationsURL(t), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo, err := postgres.NewUserRepo(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("NewUserRepo: %v", err)
	}
	defer repo.Close()

	if err := repo.Add(ctx, model.User{ID: "u-1", Name: "A", Email: "a@x.com"}); err != nil {
		t.Fatal(err)
	}
	if got := auditCount(t, dsn); got != 1 {
		t.Fatalf("after Add: audit=%d, want 1", got)
	}
	if err := repo.Remove(ctx, "u-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// FK cascade removes the audit row(s) too.
	if got := auditCount(t, dsn); got != 0 {
		t.Fatalf("after Remove: audit=%d, want 0 (cascade)", got)
	}
}

// Compile-time interface check — uses a zero pgx.Tx just to silence
// "imported and not used" without writing a runtime test for it.
var _ = pgx.ErrNoRows
