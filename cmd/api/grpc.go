package main

import (
	"context"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"

	grpctransport "github.com/ashishsinghbhadoria/goLearn/internal/transport/grpc"
	"github.com/ashishsinghbhadoria/goLearn/internal/tokens"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	userpb "github.com/ashishsinghbhadoria/goLearn/proto/gen/userpb"
)

// startGRPC binds a TCP listener and starts a gRPC server on it.
// Returns the *grpc.Server so main can call GracefulStop on shutdown.
// The server runs in its own goroutine; serving errors are logged.
func startGRPC(addr string, svc *user.Service, store *tokens.Store, logger *slog.Logger) (*grpc.Server, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(grpcAccessLog(logger)),
	)
	userpb.RegisterUserServiceServer(srv, grpctransport.NewServer(svc, store, logger))

	go func() {
		logger.Info("grpc listening", "addr", addr)
		if err := srv.Serve(lis); err != nil {
			logger.Error("grpc serve failed", "err", err)
		}
	}()
	return srv, nil
}

// grpcAccessLog mirrors the HTTP withAccessLog middleware: one
// structured line per RPC with method, status code, and duration.
func grpcAccessLog(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.Info("grpc",
			"method", info.FullMethod,
			"err", err,
			"dur", time.Since(start),
		)
		return resp, err
	}
}
