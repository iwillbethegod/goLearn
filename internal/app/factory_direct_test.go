package app_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/app"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewRepository_Memory(t *testing.T) {
	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:   app.TypeMemory,
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	if repo == nil {
		t.Fatal("memory repo nil")
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNewRepository_JSONFileExplicitPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:     app.TypeJSONFile,
		JSONPath: path,
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("jsonfile: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if repo == nil {
		t.Fatal("jsonfile repo nil")
	}
}

func TestNewRepository_JSONFileDefaultPath(t *testing.T) {
	// JSONPath empty means the factory falls back to "users.json" in
	// the CWD. We chdir to a tempdir so we don't pollute the repo root.
	dir := t.TempDir()
	cwd, err := test_chdir(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = test_chdir(cwd) })

	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:   app.TypeJSONFile,
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("jsonfile default path: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
}

func TestNewRepository_PostgresEmptyDSNErrors(t *testing.T) {
	_, err := app.NewRepository(app.RepositoryConfig{
		Type:   app.TypePostgres,
		Logger: discardLogger(),
	})
	if err == nil {
		t.Fatal("postgres with empty DSN must error")
	}
}

func TestNewRepository_PostgresNilCtxFallsBackToBackground(t *testing.T) {
	// We don't have a real Postgres available in -short, so the real
	// connection will fail; the test just verifies the nil-ctx branch
	// is reached without panicking. An empty DSN errors before any
	// connection is attempted, but a bogus DSN exercises the dial path
	// briefly.
	_, err := app.NewRepository(app.RepositoryConfig{
		Type:   app.TypePostgres,
		DSN:    "postgres://nobody:nopw@127.0.0.1:1/nodb?sslmode=disable&connect_timeout=1",
		Logger: discardLogger(),
		// Ctx left nil — covers the cfg.Ctx == nil branch.
	})
	if err == nil {
		t.Fatal("expected dial error for bogus DSN")
	}
}

func TestNewRepository_UnknownTypeErrors(t *testing.T) {
	_, err := app.NewRepository(app.RepositoryConfig{
		Type:   app.RepositoryType("nope"),
		Logger: discardLogger(),
	})
	if err == nil {
		t.Fatal("unknown type must error")
	}
}

// test_chdir is a tiny helper used in TestNewRepository_JSONFileDefaultPath
// to bracket a t.TempDir() chdir. Returns the original wd.
func test_chdir(target string) (string, error) {
	prev, err := getwd()
	if err != nil {
		return "", err
	}
	return prev, chdir(target)
}

// getwd / chdir are tiny indirections so the test file doesn't bring in
// `os` if a future linter rule blocks it; they delegate to os.* today.
func getwd() (string, error) {
	return osGetwd()
}
func chdir(s string) error {
	return osChdir(s)
}
