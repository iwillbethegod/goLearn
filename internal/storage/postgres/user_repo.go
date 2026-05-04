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

func (r *UserRepo) Get(id string) (user.User, error) {
	return user.User{}, ErrPostgresNotImplemented
}

func (r *UserRepo) GetByEmail(email string) (user.User, error) {
	return user.User{}, ErrPostgresNotImplemented
}

func (r *UserRepo) Update(u user.User) error {
	return ErrPostgresNotImplemented
}

func (r *UserRepo) List() ([]user.User, error) {
	return nil, ErrPostgresNotImplemented
}

func (r *UserRepo) Remove(userID string) error {
	return ErrPostgresNotImplemented
}

var _ user.Repository = (*UserRepo)(nil)
