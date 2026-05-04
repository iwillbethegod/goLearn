// Package main is the HTTP API server entrypoint. It wires the
// generated OpenAPI router (oapi-codegen std-http-server) to the
// day-1 user service via the day-3 httpapi.Handler.
//
// Middleware chain (outermost first):
//
//	withRecover         → panics become 500 + JSON envelope
//	withRequestID       → 8-byte hex id, propagated via ctx + header
//	withAccessLog       → one structured line per completed request
//	withBodyLimit       → 1 MiB cap on request bodies
//	withValidation      → kin-openapi spec validation (returns 400 envelope)
//	(generated router)
//	  → httpapi.Handler  → user.Service → repository
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

	"github.com/ashishsinghbhadoria/goLearn/internal/app"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	storePath := flag.String("store-path", ".data/users.json", "user store path")
	storage := flag.String("storage", "jsonfile", "storage strategy: memory or jsonfile")
	shutdownTimeout := flag.Duration("shutdown-timeout", 5*time.Second, "graceful shutdown grace period")
	readTimeout := flag.Duration("read-timeout", 10*time.Second, "max request read time including body")
	writeTimeout := flag.Duration("write-timeout", 10*time.Second, "max time before writing response")
	idleTimeout := flag.Duration("idle-timeout", 60*time.Second, "keep-alive idle timeout")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	repo, err := app.NewRepository(app.RepositoryConfig{
		Type:     app.RepositoryType(*storage),
		JSONPath: *storePath,
		Logger:   logger,
	})
	if err != nil {
		logger.Error("init repository failed", "err", err)
		os.Exit(1)
	}
	svc := user.NewService(repo, logger, metrics.New())
	h := httpapi.NewHandler(svc, logger)

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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		logger.Info("api listening",
			"addr", *addr, "storage", *storage, "store_path", *storePath,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer sCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
		fmt.Fprintln(os.Stderr, err)
	}
}
