// Package user holds the domain service + dedup gate. Data-bearing
// types (User, AppError, validation helpers) live in internal/model;
// behavior (Repository contract, Service business rules, DedupStore)
// lives here.
package user

import (
	"context"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
)

// Repository is the persistence boundary. Implementations live under
// internal/storage/{jsonfile,memory}. The interface is defined here
// (the consumer side) so a new backend doesn't have to import every
// package that uses User.
type Repository interface {
	Add(ctx context.Context, u model.User) error
	Get(ctx context.Context, id string) (model.User, error)
	GetByEmail(ctx context.Context, email string) (model.User, error)
	Update(ctx context.Context, u model.User) error
	Remove(ctx context.Context, userID string) error
	List(ctx context.Context) ([]model.User, error)
}
