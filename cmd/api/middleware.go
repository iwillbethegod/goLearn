package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
)

// Maximum accepted request body size for any endpoint. The OpenAPI
// schemas enforce per-field caps, but a global cap protects the
// process from a single 1 GB POST body.
const maxBodyBytes = 1 << 20 // 1 MiB

type ctxKey string

const ctxKeyRequestID ctxKey = "request_id"

// requestID returns the request ID stored in ctx, or "" if none.
func requestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// withRequestID generates a per-request 8-byte hex ID, attaches it to
// ctx, echoes it on the X-Request-Id response header, and prefixes
// every log line emitted under this request with it via slog attrs.
func withRequestID() gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if id == "" {
				id = newRequestID()
			}
			w.Header().Set("X-Request-Id", id)
			ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: timestamp-based (extremely unlikely path; rand.Read
		// only fails on a broken /dev/urandom).
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// withBodyLimit caps every request body at maxBodyBytes. Reads beyond
// the cap return http.ErrBodyReadAfterClose; the handler treats it as
// a malformed body (handler-side decode error → 400).
func withBodyLimit() gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// withRecover converts panics into 500 + JSON error envelope. The
// stack trace goes to the logger, never to the client.
func withRecover(logger *slog.Logger) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.ErrorContext(r.Context(), "panic recovered",
						"request_id", requestID(r.Context()),
						"method", r.Method, "path", r.URL.Path,
						"panic", fmt.Sprint(rec),
						"stack", string(debug.Stack()),
					)
					writeJSONError(w, http.StatusInternalServerError, "internal_error", "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// withAccessLog logs one structured line per request after it
// completes. Status is captured by wrapping ResponseWriter.
func withAccessLog(logger *slog.Logger) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			logger.InfoContext(r.Context(), "http",
				"request_id", requestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"dur", time.Since(start),
			)
		})
	}
}

// withValidation wraps the kin-openapi validator with a custom JSON
// error response so client-visible 400s match the spec's Error
// envelope shape rather than the validator's default plaintext.
func withValidation(swagger *openapi3.T) gen.MiddlewareFunc {
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

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}
