// Package app exposes a small factory that maps a runtime config
// (RepositoryType + JSON path) to a concrete user.Repository
// implementation. It is the strategy-pattern seam: callers depend
// on the user.Repository interface, never on a concrete backend.
package app

import (
	"fmt"
	"log/slog"

	"github.com/ashishsinghbhadoria/goLearn/internal/storage/jsonfile"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// RepositoryType selects a storage strategy.
type RepositoryType string

const (
	TypeMemory   RepositoryType = "memory"
	TypeJSONFile RepositoryType = "jsonfile"
)

// RepositoryConfig holds repository creation parameters. Only
// JSONPath is meaningful when Type == TypeJSONFile.
type RepositoryConfig struct {
	Type     RepositoryType
	JSONPath string
	Logger   *slog.Logger
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
	default:
		return nil, fmt.Errorf("unknown repository type: %s", cfg.Type)
	}
}
