// Package grpc adapts the day-1 user.Service and the day-4 token
// bucket to the gRPC contract defined in proto/user/v1/user.proto.
//
// Translation rules:
//   - Domain types (model.User, AppError) → protobuf messages.
//   - AppError codes → grpc/codes.Code via statusFor.
//   - Token-bucket logic delegates to the *tokens.Store directly;
//     it doesn't go through user.Service because tokens are an
//     orthogonal concern (not persisted with the user record yet).
package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/tokens"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	userpb "github.com/ashishsinghbhadoria/goLearn/proto/gen/userpb"
)

// errUserIDRequired is the message returned for any RPC that arrives
// with an empty user_id. Centralised so the wording stays consistent
// across all five RPCs.
const errUserIDRequired = "user_id is required"

// Server implements userpb.UserServiceServer.
//
// Embedding UnimplementedUserServiceServer is the gRPC convention
// for forward-compatibility: when we regenerate the stubs after
// adding a new RPC, the server still compiles even if we haven't
// implemented the new method yet (it returns Unimplemented).
type Server struct {
	userpb.UnimplementedUserServiceServer

	svc    *user.Service
	tokens *tokens.Store
	logger *slog.Logger
}

func NewServer(svc *user.Service, store *tokens.Store, logger *slog.Logger) *Server {
	return &Server{svc: svc, tokens: store, logger: logger}
}

// ----- Token bucket RPCs -----

func (s *Server) GetTokens(_ context.Context, req *userpb.GetTokensRequest) (*userpb.GetTokensResponse, error) {
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, errUserIDRequired)
	}
	b := s.tokens.ForUser(req.GetUserId())
	return &userpb.GetTokensResponse{
		Available: b.Available(),
		Capacity:  b.Capacity(),
	}, nil
}

func (s *Server) TakeTokens(_ context.Context, req *userpb.TakeTokensRequest) (*userpb.TakeTokensResponse, error) {
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, errUserIDRequired)
	}
	if req.GetCount() < 0 {
		return nil, status.Error(codes.InvalidArgument, "count must be >= 0")
	}
	b := s.tokens.ForUser(req.GetUserId())
	granted, remaining := b.Take(req.GetCount())
	if !granted {
		s.logger.Info("tokens denied",
			"user_id", req.GetUserId(),
			"requested", req.GetCount(),
			"available", remaining,
		)
	}
	return &userpb.TakeTokensResponse{Granted: granted, Remaining: remaining}, nil
}

func (s *Server) ReturnTokens(_ context.Context, req *userpb.ReturnTokensRequest) (*userpb.ReturnTokensResponse, error) {
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, errUserIDRequired)
	}
	if req.GetCount() < 0 {
		return nil, status.Error(codes.InvalidArgument, "count must be >= 0")
	}
	b := s.tokens.ForUser(req.GetUserId())
	rem := b.Return(req.GetCount())
	s.logger.Debug("tokens returned",
		"user_id", req.GetUserId(),
		"count", req.GetCount(),
		"remaining", rem,
	)
	return &userpb.ReturnTokensResponse{Remaining: rem}, nil
}

// ----- CRUD RPCs (mirror REST) -----

func (s *Server) GetUser(ctx context.Context, req *userpb.GetUserRequest) (*userpb.User, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	u, err := s.svc.GetUser(ctx, req.GetId())
	if err != nil {
		return nil, statusFor(err)
	}
	return toPBUser(u), nil
}

func (s *Server) ListUsers(ctx context.Context, req *userpb.ListUsersRequest) (*userpb.ListUsersResponse, error) {
	limit, offset := paginationParams(req.GetLimit(), req.GetOffset())
	users, total, err := s.svc.ListUsers(ctx, limit, offset)
	if err != nil {
		return nil, statusFor(err)
	}
	out := make([]*userpb.User, 0, len(users))
	for _, u := range users {
		out = append(out, toPBUser(u))
	}
	return &userpb.ListUsersResponse{Users: out, Total: total}, nil
}

// ----- helpers -----

const (
	defaultPageLimit = 100
	maxPageLimit     = 1000
)

func paginationParams(limit, offset int32) (int, int) {
	l := int(limit)
	if l < 1 {
		l = defaultPageLimit
	}
	if l > maxPageLimit {
		l = maxPageLimit
	}
	o := int(offset)
	if o < 0 {
		o = 0
	}
	return l, o
}

// toPBUser strips PasswordHash from the wire format. The .proto
// User message doesn't have a password_hash field, so the conversion
// is one-way and password material can never accidentally leak via
// gRPC.
func toPBUser(u model.User) *userpb.User {
	return &userpb.User{
		Id:    u.ID,
		Name:  u.Name,
		Email: u.Email,
	}
}

// statusFor maps a domain error to a grpc/status. Storage errors
// are scrubbed for the wire ("storage error") and logged separately
// by interceptors at the call site.
func statusFor(err error) error {
	var appErr *model.AppError
	if !errors.As(err, &appErr) {
		return status.Error(codes.Internal, "internal error")
	}
	switch appErr.Code {
	case model.CodeUserNotFound:
		return status.Error(codes.NotFound, appErr.Message)
	case model.CodeDuplicateUser:
		return status.Error(codes.AlreadyExists, appErr.Message)
	case model.CodeInvalidUser, model.CodeInvalidEmail, model.CodeInvalidPassword:
		return status.Error(codes.InvalidArgument, appErr.Message)
	case model.CodeInvalidCredential:
		return status.Error(codes.Unauthenticated, appErr.Message)
	case model.CodeStorage:
		return status.Error(codes.Internal, "storage error")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
