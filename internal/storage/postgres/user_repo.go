// Package postgres is the Day-5 backend for user.Repository: a
// pgxpool-backed implementation that uses sqlc-generated typed
// queries (internal/storage/postgres/pgdb) and maps Postgres errors
// to the project's model.AppError taxonomy so the REST and gRPC
// transports keep working unchanged.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres/pgdb"
)

// UserRepo persists model.User to Postgres via pgxpool + sqlc.
type UserRepo struct {
	pool   *pgxpool.Pool
	q      *pgdb.Queries
	logger *slog.Logger
}

// NewUserRepo opens a pgxpool connection at dsn, pings to verify
// reachability, and returns a connected repository. The caller owns
// the lifecycle: call Close when done.
func NewUserRepo(ctx context.Context, dsn string, logger *slog.Logger) (*UserRepo, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres: empty DSN (set -db-dsn or $DATABASE_URL)")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, model.NewStorageError(fmt.Errorf("pgxpool.New: %w", err))
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, model.NewStorageError(fmt.Errorf("pgxpool.Ping: %w", err))
	}
	return &UserRepo{pool: pool, q: pgdb.New(pool), logger: logger}, nil
}

// Close releases the underlying connection pool.
func (r *UserRepo) Close() { r.pool.Close() }

// Add persists a user inside a single transaction that also writes
// to registration_log. Either both rows land or neither — the Day-5
// "transactional user creation" deliverable in concrete form.
func (r *UserRepo) Add(ctx context.Context, u model.User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit

	qtx := r.q.WithTx(tx)
	if err := qtx.AddUser(ctx, pgdb.AddUserParams{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
	}); err != nil {
		return mapErr(err)
	}
	if err := qtx.LogRegistration(ctx, pgdb.LogRegistrationParams{
		UserID: u.ID,
		Event:  "register",
	}); err != nil {
		return mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapErr(err)
	}
	r.logger.Info("user persisted", "user_id", u.ID)
	return nil
}

func (r *UserRepo) Get(ctx context.Context, id string) (model.User, error) {
	if err := ctx.Err(); err != nil {
		return model.User{}, err
	}
	if strings.TrimSpace(id) == "" {
		return model.User{}, model.ErrInvalidUser
	}
	row, err := r.q.GetUserByID(ctx, id)
	if err != nil {
		return model.User{}, mapErr(err)
	}
	return toModel(row), nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	if err := ctx.Err(); err != nil {
		return model.User{}, err
	}
	if strings.TrimSpace(email) == "" {
		return model.User{}, model.ErrInvalidEmail
	}
	row, err := r.q.GetUserByEmail(ctx, email)
	if err != nil {
		return model.User{}, mapErr(err)
	}
	return toModel(row), nil
}

// Update overwrites name + email + password_hash for u.ID. An empty
// PasswordHash on the incoming User preserves the stored one
// (matches the memory + jsonfile semantics).
func (r *UserRepo) Update(ctx context.Context, u model.User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := r.q.GetUserByID(ctx, u.ID)
	if err != nil {
		return mapErr(err)
	}
	pwd := u.PasswordHash
	if pwd == "" {
		pwd = current.PasswordHash
	}
	if err := r.q.UpdateUser(ctx, pgdb.UpdateUserParams{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		PasswordHash: pwd,
	}); err != nil {
		return mapErr(err)
	}
	r.logger.Info("user persisted on update", "user_id", u.ID)
	return nil
}

// Remove deletes by ID, verifying the row exists first so we can
// return a clean ErrUserNotFound rather than a silent success.
func (r *UserRepo) Remove(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(userID) == "" {
		return model.ErrInvalidUser
	}
	if _, err := r.q.GetUserByID(ctx, userID); err != nil {
		return mapErr(err)
	}
	if err := r.q.DeleteUser(ctx, userID); err != nil {
		return mapErr(err)
	}
	r.logger.Info("user deleted", "user_id", userID)
	return nil
}

// listMaxLimit caps the per-page row count so a misbehaving caller
// can't drag the entire users table over the wire by passing a giant
// limit. limit <= 0 from the caller is treated as "give me one full
// safety-capped page".
const listMaxLimit = 1000

func (r *UserRepo) List(ctx context.Context, limit, offset int) ([]model.User, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > listMaxLimit {
		limit = listMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.q.ListUsers(ctx, pgdb.ListUsersParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	})
	if err != nil {
		return nil, 0, mapErr(err)
	}
	total, err := r.q.CountUsers(ctx)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	out := make([]model.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, toModel(row))
	}
	return out, total, nil
}

// mapErr translates pgx + Postgres SQLSTATE errors into the
// project's domain error taxonomy. Only codes we actively handle are
// listed; everything else is opaque and wrapped as a storage error
// (which the transport layer renders as 500 / Internal).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrUserNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return model.ErrDuplicateUser
		case "23503": // foreign_key_violation
			return model.ErrInvalidUser
		}
	}
	return model.NewStorageError(err)
}

func toModel(u pgdb.User) model.User {
	return model.User{
		ID:           u.ID,
		Name:         u.Name,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
	}
}
