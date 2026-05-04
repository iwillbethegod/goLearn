package grpc

import (
	"context"
	"log/slog"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

type UserHandler struct {
	service *user.Service
	logger  *slog.Logger
}

type AddUserRequest struct {
	Name  string
	Email string
}

type AddUserResponse struct {
	ID    string
	Error string
}

type ListUsersResponse struct {
	Users []user.User
}

type Empty struct{}

func NewUserHandler(service *user.Service, logger *slog.Logger) *UserHandler {
	return &UserHandler{service: service, logger: logger}
}

func (h *UserHandler) AddUser(ctx context.Context, request *AddUserRequest) (*AddUserResponse, error) {
	user, err := h.service.AddUser(ctx, request.Name, request.Email)
	if err != nil {
		h.logger.Error("grpc add user failed", "error", err)
		return &AddUserResponse{Error: err.Error()}, err
	}

	h.logger.Info("grpc user added", "user_id", user.ID)
	return &AddUserResponse{ID: user.ID}, nil
}

func (h *UserHandler) ListUsers(ctx context.Context, _ *Empty) (*ListUsersResponse, error) {
	users, err := h.service.ListUsers()
	if err != nil {
		h.logger.Error("grpc list users failed", "error", err)
		return nil, err
	}

	return &ListUsersResponse{Users: users}, nil
}
