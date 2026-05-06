package postgres

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"  // owns "pgx5://"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // owns "postgres://" and "postgresql://"
	_ "github.com/golang-migrate/migrate/v4/source/file"      // register file:// source
)

// Migrate applies every pending migration from sourceURL against the
// database at dsn. It is a thin convenience wrapper around the
// golang-migrate library so cmd/api and the tests share one code
// path with the `make migrate-up` CLI.
//
//	sourceURL: e.g. "file://./db/migrations" (relative to CWD).
//	dsn:       e.g. "pgx5://app:app@localhost:5432/app?sslmode=disable".
//	            The "pgx5://" scheme tells golang-migrate to use the
//	            pgx-v5 driver registered above. A plain
//	            "postgres://..." DSN works too — the driver auto-
//	            registers under both names.
//
// Returns nil when the schema is already at the latest version.
func Migrate(sourceURL, dsn string) error {
	if dsn == "" {
		return errors.New("postgres.Migrate: empty DSN")
	}
	m, err := migrate.New(sourceURL, dsn)
	if err != nil {
		return fmt.Errorf("migrate.New(%s): %w", sourceURL, err)
	}
	defer func() {
		_, _ = m.Close()
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}
