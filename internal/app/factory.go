// Package app exposes a small factory that maps a runtime config
// (RepositoryType + a DSN or JSON path) to a concrete user.Repository
// implementation. It is the strategy-pattern seam: callers depend on
// the user.Repository interface, never on a concrete backend.
package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ashishsinghbhadoria/goLearn/internal/storage/jsonfile"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// RepositoryType selects a storage strategy.
type RepositoryType string

const (
	TypeMemory   RepositoryType = "memory"
	TypeJSONFile RepositoryType = "jsonfile"
	TypePostgres RepositoryType = "postgres"
)

// RepositoryConfig holds repository creation parameters. Only the
// fields meaningful for the chosen Type are read.
type RepositoryConfig struct {
	Type RepositoryType
	// JSONPath is read by the jsonfile backend (default "users.json").
	JSONPath string
	// DSN is read by the postgres backend (e.g. $DATABASE_URL).
	DSN string
	// Ctx is used by backends that need cancellable connection setup
	// (postgres). nil falls back to context.Background() so existing
	// callers keep compiling.
	Ctx    context.Context
	Logger *slog.Logger
}

// NewRepository constructs the repository selected by cfg.Type.
func NewRepository(cfg RepositoryConfig) (user.Repository, error) {
	switch cfg.Type {
	case TypeMemory:
		return memory.NewUserRepo(cfg.Logger), nil
	case TypeJSONFile:
		if cfg.JSONPath == "" {
			cfg.JSONPath = "users.json"
		}
		return jsonfile.NewUserRepo(cfg.JSONPath, cfg.Logger)
	case TypePostgres:
		ctx := cfg.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		return postgres.NewUserRepo(ctx, cfg.DSN, cfg.Logger)
	default:
		return nil, fmt.Errorf("unknown repository type: %s", cfg.Type)
	}
}
 