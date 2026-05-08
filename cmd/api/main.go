// Package main is the user-service entrypoint. It hosts two
// listeners over the same user.Service + token store:
//
//   - HTTP REST on -addr (default :8080) — see middleware chain below.
//   - gRPC unary RPCs on -grpc-addr (default :9090). The generated
//     UserService stubs live in proto/gen/userpb; the impl is in
//     internal/transport/grpc.
//
// HTTP middleware chain (outermost first):
//
//	withRecover         → panics become 500 + JSON envelope
//	withRequestID       → 8-byte hex id, propagated via ctx + header
//	withAccessLog       → one structured line per completed request
//	withBodyLimit       → 1 MiB cap on request bodies
//	withValidation      → kin-openapi spec validation (returns 400 envelope)
//	(generated router)
//	  → httpapi.Handler  → user.Service → repository
//
// gRPC interceptor chain:
//
//	grpcAccessLog       → one structured line per completed RPC
//	(generated dispatch)
//	  → grpctransport.Server → user.Service / tokens.Store
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/ashishsinghbhadoria/goLearn/internal/app"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres"
	"github.com/ashishsinghbhadoria/goLearn/internal/tokens"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	grpcAddr := flag.String("grpc-addr", ":9090", "gRPC listen address (empty disables gRPC)")
	storePath := flag.String("store-path", ".data/users.json", "user store path (jsonfile only)")
	storage := flag.String("storage", "jsonfile", "storage strategy: memory | jsonfile | postgres")
	dbDSN := flag.String("db-dsn", os.Getenv("DATABASE_URL"), "Postgres DSN; defaults to $DATABASE_URL")
	migrate := flag.Bool("migrate", false, "apply DB migrations from -migrations-path before serving (postgres only)")
	migrationsPath := flag.String("migrations-path", "file://./db/migrations", "migrate source URL (postgres only)")
	tokensCfgPath := flag.String("tokens-config", "config/tokens.yaml", "token bucket YAML config (env vars override)")
	shutdownTimeout := flag.Duration("shutdown-timeout", 5*time.Second, "graceful shutdown grace period")
	readTimeout := flag.Duration("read-timeout", 10*time.Second, "max request read time including body")
	writeTimeout := flag.Duration("write-timeout", 10*time.Second, "max time before writing response")
	idleTimeout := flag.Duration("idle-timeout", 60*time.Second, "keep-alive idle timeout")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	rootCtx, rootCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer rootCancel()

	if *storage == string(app.TypePostgres) && *migrate {
		if err := postgres.Migrate(*migrationsPath, *dbDSN); err != nil {
			logger.Error("db migrate failed", "err", err)
			os.Exit(1)
		}
		logger.Info("db migrated", "source", *migrationsPath)
	}

	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:     app.RepositoryType(*storage),
		JSONPath: *storePath,
		DSN:      *dbDSN,
		Ctx:      rootCtx,
		Logger:   logger,
	})
	if err != nil {
		logger.Error("init repository failed", "err", err)
		os.Exit(1)
	}
	svc := user.NewService(repo, logger, metrics.New())
	h := httpapi.NewHandler(svc, logger)

	tokensCfg, err := tokens.Load(*tokensCfgPath)
	if err != nil {
		logger.Error("load tokens config failed", "err", err)
		os.Exit(1)
	}
	tokenStore := tokens.NewStore(tokensCfg)
	logger.Info("tokens config",
		"capacity", tokensCfg.Capacity,
		"rate_per_min", tokensCfg.RatePerMin,
	)

	swagger, err := gen.GetSwagger()
	if err != nil {
		logger.Error("load embedded openapi spec", "err", err)
		os.Exit(1)
	}
	swagger.Servers = nil // accept any host (validator otherwise rejects /)

	mux := http.NewServeMux()
	gen.HandlerWithOptions(h, gen.StdHTTPServerOptions{
		BaseRouter: mux,
		Middlewares: []gen.MiddlewareFunc{
			withRecover(logger),
			withRequestID(),
			withAccessLog(logger),
			withBodyLimit(),
			withValidation(swagger),
		},
		ErrorHandlerFunc: writeRouterError,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       *readTimeout,
		WriteTimeout:      *writeTimeout,
		IdleTimeout:       *idleTimeout,
		MaxHeaderBytes:    1 << 14, // 16 KiB
	}

	go func() {
		logger.Info("api listening",
			"addr", *addr, "storage", *storage, "store_path", *storePath,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			rootCancel()
		}
	}()

	var grpcSrv *grpc.Server
	if *grpcAddr != "" {
		grpcSrv, err = startGRPC(*grpcAddr, svc, tokenStore, logger)
		if err != nil {
			logger.Error("grpc listen failed", "addr", *grpcAddr, "err", err)
			os.Exit(1)
		}
	}

	<-rootCtx.Done()
	logger.Info("shutting down")
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer sCancel()
	if grpcSrv != nil {
		done := make(chan struct{})
		go func() {
			grpcSrv.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-shutdownCtx.Done():
			logger.Warn("grpc graceful stop timed out; forcing", "timeout", *shutdownTimeout)
			grpcSrv.Stop()
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
		fmt.Fprintln(os.Stderr, err)
	}
}
