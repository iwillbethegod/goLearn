package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

type UserRepo struct {
	db *sql.DB
}

var ErrPostgresNotImplemented = errors.New("postgres repository not implemented")

func NewUserRepo(db *sql.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) Add(ctx context.Context, u model.User) error {
	return ErrPostgresNotImplemented
}

func (r *UserRepo) Get(ctx context.Context, id string) (model.User, error) {
	return model.User{}, ErrPostgresNotImplemented
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	return model.User{}, ErrPostgresNotImplemented
}

func (r *UserRepo) Update(ctx context.Context, u model.User) error {
	return ErrPostgresNotImplemented
}

func (r *UserRepo) List(ctx context.Context) ([]model.User, error) {
	return nil, ErrPostgresNotImplemented
}

func (r *UserRepo) Remove(ctx context.Context, userID string) error {
	return ErrPostgresNotImplemented
}

var _ user.Repository = (*UserRepo)(nil)
