package user

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

// MinPasswordLen is the shortest password the Service will accept on
// registration. 8 is a defensible default for a learning project.
const MinPasswordLen = 8

const (
	loginFailedMsg     = "login failed"
	invalidEmailFmtMsg = "invalid email format"
)

// dummyHash is generated once at init time and used as a constant-time
// stand-in when Login fails to find the user. Without this, a bcrypt
// compare runs only on real users (~60 ms), so an attacker can
// distinguish "user exists" from "user doesn't" by request latency.
// Comparing against dummyHash keeps the wall time statistically
// uniform across both branches.
var dummyHash []byte

func init() {
	h, err := bcrypt.GenerateFromPassword([]byte("constant-time-dummy"), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt.GenerateFromPassword on a fixed input cannot fail in
		// practice; panicking here would be surprising at startup.
		// Set to a known-bad value so subsequent compares simply fail.
		dummyHash = []byte("invalid")
		return
	}
	dummyHash = h
}

type Service struct {
	repo    Repository
	logger  *slog.Logger
	metrics *metrics.Metrics
}

func NewService(repo Repository, logger *slog.Logger, metricsCollector *metrics.Metrics) *Service {
	return &Service{
		repo:    repo,
		logger:  logger,
		metrics: metricsCollector,
	}
}

func (s *Service) Logger() *slog.Logger {
	return s.logger
}

func (s *Service) AddUser(ctx context.Context, name, email string) (model.User, error) {
	trimmedName := strings.TrimSpace(name)
	trimmedEmail := strings.TrimSpace(email)
	if trimmedName == "" || trimmedEmail == "" {
		s.logger.Error("invalid add user request", "error", model.ErrInvalidUser)
		return model.User{}, model.ErrInvalidUser
	}

	if !model.IsValidEmail(trimmedEmail) {
		s.logger.Error(invalidEmailFmtMsg, "error", model.ErrInvalidEmail, "email", trimmedEmail)
		return model.User{}, model.ErrInvalidEmail
	}

	newUser := model.User{
		ID:    s.generateID(),
		Name:  trimmedName,
		Email: trimmedEmail,
	}

	if err := s.repo.Add(ctx, newUser); err != nil {
		s.logger.Error("repository failed to add user", "error", err, "user_id", newUser.ID)
		return model.User{}, err
	}

	s.metrics.IncUserAdded()
	s.logger.Info("user created", "user_id", newUser.ID)
	return newUser, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]model.User, error) {
	users, err := s.repo.List(ctx)
	if err != nil {
		s.logger.Error("repository failed to list users", "error", err)
		return nil, err
	}
	return users, nil
}

func (s *Service) RemoveUser(ctx context.Context, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		s.logger.Error("invalid remove user request", "error", model.ErrInvalidUser)
		return model.ErrInvalidUser
	}

	if err := s.repo.Remove(ctx, userID); err != nil {
		s.logger.Error("repository failed to remove user", "error", err, "user_id", userID)
		return err
	}

	s.logger.Info("user removed", "user_id", userID)
	return nil
}

// generateID returns a "u-" prefix followed by 24 hex chars (12
// random bytes / 96 bits). The previous time.Now().UnixNano() scheme
// could collide when two registrations landed in the same nanosecond.
func (s *Service) generateID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// /dev/urandom is effectively never broken on production
		// systems; if it is, fall back to a still-unique enough ID
		// rather than panicking.
		s.logger.Error("rand.Read failed, falling back", "err", err)
		return "u-fallback-" + hex.EncodeToString(b[:])
	}
	return "u-" + hex.EncodeToString(b[:])
}

// GetUser returns the user with the given ID.
func (s *Service) GetUser(ctx context.Context, id string) (model.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return model.User{}, model.ErrInvalidUser
	}
	u, err := s.repo.Get(ctx, id)
	if err != nil {
		s.logger.Debug("repository get failed", "error", err, "user_id", id)
		return model.User{}, err
	}
	return u, nil
}

// UpdateUser modifies the name and/or email on an existing user.
// An empty name or email leaves that field unchanged. Email changes
// are validated for format and checked for collision against other
// users by the repository.
func (s *Service) UpdateUser(ctx context.Context, id, name, email string) (model.User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return model.User{}, model.ErrInvalidUser
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return model.User{}, err
	}
	trimmedName := strings.TrimSpace(name)
	trimmedEmail := strings.TrimSpace(email)
	if trimmedName != "" {
		current.Name = trimmedName
	}
	if trimmedEmail != "" {
		if !model.IsValidEmail(trimmedEmail) {
			s.logger.Error(invalidEmailFmtMsg, "error", model.ErrInvalidEmail, "email", trimmedEmail)
			return model.User{}, model.ErrInvalidEmail
		}
		current.Email = trimmedEmail
	}
	if err := s.repo.Update(ctx, current); err != nil {
		s.logger.Error("repository failed to update user", "error", err, "user_id", id)
		return model.User{}, err
	}
	s.logger.Info("user updated", "user_id", id)
	return current, nil
}

// Register creates a new user with a bcrypt-hashed password and
// persists them via the repository.
func (s *Service) Register(ctx context.Context, name, email, password string) (model.User, error) {
	trimmedName := strings.TrimSpace(name)
	trimmedEmail := strings.TrimSpace(email)
	if trimmedName == "" || trimmedEmail == "" {
		s.logger.Error("invalid register request", "error", model.ErrInvalidUser)
		return model.User{}, model.ErrInvalidUser
	}
	if !model.IsValidEmail(trimmedEmail) {
		s.logger.Error(invalidEmailFmtMsg, "error", model.ErrInvalidEmail, "email", trimmedEmail)
		return model.User{}, model.ErrInvalidEmail
	}
	if len(password) < MinPasswordLen {
		s.logger.Error("password too short", "min", MinPasswordLen)
		return model.User{}, model.ErrInvalidPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.logger.Error("hash password failed", "error", err)
		return model.User{}, model.NewStorageError(err)
	}

	newUser := model.User{
		ID:           s.generateID(),
		Name:         trimmedName,
		Email:        trimmedEmail,
		PasswordHash: string(hash),
	}

	if err := s.repo.Add(ctx, newUser); err != nil {
		s.logger.Error("repository failed to add user", "error", err, "user_id", newUser.ID)
		return model.User{}, err
	}

	s.metrics.IncUserAdded()
	s.logger.Info("user registered", "user_id", newUser.ID, "email", newUser.Email)
	return newUser, nil
}

// Login looks up the user by email and verifies the password against
// the stored bcrypt hash. Wrong email and wrong password both return
// model.ErrInvalidCredential to avoid leaking which one was wrong.
//
// The not-found branch deliberately runs a bcrypt compare against a
// dummy hash so the response time is statistically uniform with the
// real-user / wrong-password branch — closes a timing side-channel.
func (s *Service) Login(ctx context.Context, email, password string) (model.User, error) {
	u, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, model.ErrUserNotFound) || errors.Is(err, model.ErrInvalidEmail) {
			// Constant-time compensation: keep wall time comparable to
			// the real-user path so an attacker can't distinguish.
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
			s.logger.Warn(loginFailedMsg, "reason", "no such user", "email", email)
			return model.User{}, model.ErrInvalidCredential
		}
		s.logger.Error("repository lookup failed", "error", err)
		return model.User{}, err
	}
	if u.PasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		s.logger.Warn(loginFailedMsg, "reason", "user has no password set", "email", email)
		return model.User{}, model.ErrInvalidCredential
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.logger.Warn(loginFailedMsg, "reason", "bad password", "email", email)
		return model.User{}, model.ErrInvalidCredential
	}
	s.logger.Info("login ok", "user_id", u.ID, "email", u.Email)
	return u, nil
}

// DeleteByEmail authenticates the email/password pair and then
// removes the user. Returns model.ErrInvalidCredential on any auth
// failure.
func (s *Service) DeleteByEmail(ctx context.Context, email, password string) error {
	u, err := s.Login(ctx, email, password)
	if err != nil {
		return err
	}
	if err := s.repo.Remove(ctx, u.ID); err != nil {
		s.logger.Error("repository failed to remove user", "error", err, "user_id", u.ID)
		return err
	}
	s.logger.Info("user deleted", "user_id", u.ID, "email", u.Email)
	return nil
}
