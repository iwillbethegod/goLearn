package app

import (
	"fmt"
	"log/slog"

	"github.com/ashishsinghbhadoria/goLearn/internal/storage/jsonfile"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/memory"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// RepositoryType defines the storage strategy
type RepositoryType string

const (
	TypeMemory   RepositoryType = "memory"
	TypeJSONFile RepositoryType = "jsonfile"
	TypePostgres RepositoryType = "postgres"
)

// RepositoryConfig holds repository creation parameters
type RepositoryConfig struct {
	Type     RepositoryType
	JSONPath string // for jsonfile strategy
	Logger   *slog.Logger
}

// NewRepository creates a repository based on the strategy specified in RepositoryConfig
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
		// TODO: implement postgres
		return nil, fmt.Errorf("postgres repository not yet implemented")

	default:
		return nil, fmt.Errorf("unknown repository type: %s", cfg.Type)
	}
}
