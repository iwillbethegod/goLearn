// Package main is the user-service entrypoint. It hosts two
// listeners over the same user.Service + token store:
//
//   - HTTP REST on -addr (default :8080) — see middleware chain below.
//   - gRPC unary RPCs on -grpc-addr (default :9090). The generated
//     UserService stubs live in proto/gen/userpb; the impl is in
//     internal/transport/grpc.
//
// Day 6: every init step now returns through run() so deferred
// shutdowns (TracerProvider flush, repo close, gRPC graceful stop,
// HTTP shutdown) actually fire on every error path. The previous
// os.Exit(1) cascade dropped buffered spans on the floor.
//
// HTTP middleware chain (outermost first):
//
//	otelhttp            → root span "POST /users" etc. (Day 6)
//	withRecover         → panics become 500 + JSON envelope
//	withRequestID       → 8-byte hex id, propagated via ctx + header
//	withAccessLog       → one structured line per completed request
//	withBodyLimit       → 1 MiB cap on request bodies
//	withValidation      → kin-openapi spec validation
//	(generated router)  → httpapi.Handler → user.Service → repository
//
// gRPC interceptor chain:
//
//	otelgrpc StatsHandler  → root RPC span (Day 6)
//	grpcAccessLog          → one structured line per RPC
//	(generated dispatch)   → grpctransport.Server → user.Service / tokens.Store
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
	natsevents "github.com/ashishsinghbhadoria/goLearn/internal/events/nats"
	"github.com/ashishsinghbhadoria/goLearn/internal/observability"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres"
	"github.com/ashishsinghbhadoria/goLearn/internal/tokens"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	pkglogger "github.com/ashishsinghbhadoria/goLearn/pkg/logger"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

type apiConfig struct {
	addr            string
	grpcAddr        string
	storePath       string
	storage         string
	dbDSN           string
	migrate         bool
	migrationsPath  string
	tokensCfgPath   string
	otelService     string
	otelEndpoint    string
	otelExporter    string
	natsURL         string
	shutdownTimeout time.Duration
	readTimeout     time.Duration
	writeTimeout    time.Duration
	idleTimeout     time.Duration
}

func parseAPIFlags() apiConfig {
	cfg := apiConfig{}
	flag.StringVar(&cfg.addr, "addr", ":8080", "HTTP listen address")
	flag.StringVar(&cfg.grpcAddr, "grpc-addr", ":9090", "gRPC listen address (empty disables gRPC)")
	flag.StringVar(&cfg.storePath, "store-path", ".data/users.json", "user store path (jsonfile only)")
	flag.StringVar(&cfg.storage, "storage", "jsonfile", "storage strategy: memory | jsonfile | postgres")
	flag.StringVar(&cfg.dbDSN, "db-dsn", os.Getenv("DATABASE_URL"), "Postgres DSN; defaults to $DATABASE_URL")
	flag.BoolVar(&cfg.migrate, "migrate", false, "apply DB migrations from -migrations-path before serving (postgres only)")
	flag.StringVar(&cfg.migrationsPath, "migrations-path", "file://./db/migrations", "migrate source URL (postgres only)")
	flag.StringVar(&cfg.tokensCfgPath, "tokens-config", "config/tokens.yaml", "token bucket YAML config (env vars override)")
	flag.StringVar(&cfg.otelService, "otel-service-name", firstNonEmpty(os.Getenv("OTEL_SERVICE_NAME"), "goLearn-api"), "OTel service.name resource attr")
	flag.StringVar(&cfg.otelEndpoint, "otel-endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), "OTLP gRPC endpoint (e.g. localhost:4317); empty disables OTLP")
	flag.StringVar(&cfg.otelExporter, "otel-exporter", os.Getenv("OTEL_TRACES_EXPORTER"), "trace exporter: otlp | stdout | none (defaults to otlp when -otel-endpoint is set)")
	flag.StringVar(&cfg.natsURL, "nats-url", os.Getenv("NATS_URL"), "NATS JetStream URL (e.g. nats://localhost:4222); empty disables event publishing")
	flag.DurationVar(&cfg.shutdownTimeout, "shutdown-timeout", 5*time.Second, "graceful shutdown grace period")
	flag.DurationVar(&cfg.readTimeout, "read-timeout", 10*time.Second, "max request read time including body")
	flag.DurationVar(&cfg.writeTimeout, "write-timeout", 10*time.Second, "max time before writing response")
	flag.DurationVar(&cfg.idleTimeout, "idle-timeout", 60*time.Second, "keep-alive idle timeout")
	flag.Parse()
	return cfg
}

func run() error {
	cfg := parseAPIFlags()

	logger := slog.New(pkglogger.NewTraceHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
	))

	rootCtx, rootCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer rootCancel()

	otelShutdown, err := observability.Init(rootCtx, observability.Config{
		ServiceName: cfg.otelService,
		Endpoint:    cfg.otelEndpoint,
		Exporter:    cfg.otelExporter,
	})
	if err != nil {
		return fmt.Errorf("observability init: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("otel shutdown", "err", err)
		}
	}()

	if cfg.storage == string(app.TypePostgres) && cfg.migrate {
		if err := postgres.Migrate(cfg.migrationsPath, cfg.dbDSN); err != nil {
			return fmt.Errorf("db migrate: %w", err)
		}
		logger.Info("db migrated", "source", cfg.migrationsPath)
	}

	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:     app.RepositoryType(cfg.storage),
		JSONPath: cfg.storePath,
		DSN:      cfg.dbDSN,
		Ctx:      rootCtx,
		Logger:   logger,
	})
	if err != nil {
		return fmt.Errorf("init repository: %w", err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			logger.Error("repository close", "err", err)
		}
	}()

	var svcOpts []user.Option
	if cfg.natsURL != "" {
		pub, err := natsevents.NewPublisher(rootCtx, cfg.natsURL, logger)
		if err != nil {
			return fmt.Errorf("nats publisher: %w", err)
		}
		defer func() {
			if err := pub.Close(); err != nil {
				logger.Error("nats publisher close", "err", err)
			}
		}()
		svcOpts = append(svcOpts, user.WithPublisher(pub))
		logger.Info("nats publisher wired", "url", cfg.natsURL, "stream", natsevents.StreamName)
	}

	svc := user.NewService(repo, logger, metrics.New(), svcOpts...)
	h := httpapi.NewHandler(svc, logger)

	tokensCfg, err := tokens.Load(cfg.tokensCfgPath)
	if err != nil {
		return fmt.Errorf("load tokens config: %w", err)
	}
	tokenStore := tokens.NewStore(tokensCfg)
	logger.Info("tokens config",
		"capacity", tokensCfg.Capacity,
		"rate_per_min", tokensCfg.RatePerMin,
	)

	swagger, err := gen.GetSwagger()
	if err != nil {
		return fmt.Errorf("load embedded openapi spec: %w", err)
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
		Addr:              cfg.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.readTimeout,
		WriteTimeout:      cfg.writeTimeout,
		IdleTimeout:       cfg.idleTimeout,
		MaxHeaderBytes:    1 << 14, // 16 KiB
	}

	go func() {
		logger.Info("api listening",
			"addr", cfg.addr, "storage", cfg.storage, "store_path", cfg.storePath,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			rootCancel()
		}
	}()

	var grpcSrv *grpc.Server
	if cfg.grpcAddr != "" {
		grpcSrv, err = startGRPC(cfg.grpcAddr, svc, tokenStore, logger)
		if err != nil {
			return fmt.Errorf("grpc listen %s: %w", cfg.grpcAddr, err)
		}
	}

	<-rootCtx.Done()
	logger.Info("shutting down")
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
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
			logger.Warn("grpc graceful stop timed out; forcing", "timeout", cfg.shutdownTimeout)
			grpcSrv.Stop()
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
