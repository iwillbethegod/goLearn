package postgres

import (
	"database/sql"
	"errors"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

type UserRepo struct {
	db *sql.DB
}

var ErrPostgresNotImplemented = errors.New("postgres repository not implemented")

func NewUserRepo(db *sql.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) Add(u user.User) error {
	return ErrPostgresNotImplemented
}

func (r *UserRepo) List() ([]user.User, error) {
	return nil, ErrPostgresNotImplemented
}

var _ user.Repository = (*UserRepo)(nil)
