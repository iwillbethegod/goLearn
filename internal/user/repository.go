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
	// List returns a page of users plus the unfiltered total row count.
	// limit <= 0 means "no cap" (subject to backend-specific safety
	// ceilings); offset < 0 is treated as 0.
	List(ctx context.Context, limit, offset int) (items []model.User, total int64, err error)
}
