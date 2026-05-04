// Package main is the HTTP API server entrypoint. It wires the
// generated OpenAPI router (oapi-codegen std-http-server) to the
// day-1 user service via the day-3 httpapi.Handler.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

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
	// The spec declares servers; clearing them lets the validator
	// accept any host (localhost, 127.0.0.1, behind a proxy, ...).
	swagger.Servers = nil

	mux := http.NewServeMux()
	gen.HandlerWithOptions(h, gen.StdHTTPServerOptions{
		BaseRouter: mux,
		Middlewares: []gen.MiddlewareFunc{
			validationMiddleware(swagger),
			accessLog(logger),
		},
		ErrorHandlerFunc: writeRouterError,
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
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

// validationMiddleware wraps the kin-openapi validator with a custom
// JSON error response so client-visible 400s match the spec's Error
// envelope shape rather than the validator's default plaintext.
func validationMiddleware(swagger *openapi3.T) gen.MiddlewareFunc {
	opts := &nethttpmiddleware.Options{
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, _ nethttpmiddleware.ErrorHandlerOpts) {
			writeJSONError(w, http.StatusBadRequest, "validation_failed", err.Error())
		},
	}
	return nethttpmiddleware.OapiRequestValidatorWithOptions(swagger, opts)
}

// writeRouterError handles errors from the generated router itself
// (e.g. invalid path-param format detected by the wrapper before the
// validator middleware runs).
func writeRouterError(w http.ResponseWriter, _ *http.Request, err error) {
	writeJSONError(w, http.StatusBadRequest, "validation_failed", err.Error())
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    code,
		"message": message,
	})
}

// accessLog logs one line per request after it completes. The status
// code is captured by wrapping the ResponseWriter.
func accessLog(logger *slog.Logger) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			logger.Info("http",
				"method", r.Method, "path", r.URL.Path,
				"status", rw.status, "dur", time.Since(start),
			)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}
