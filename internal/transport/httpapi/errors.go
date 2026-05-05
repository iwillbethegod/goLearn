// Package httpapi implements the HTTP transport layer. It owns the
// concrete handler that satisfies the generated ServerInterface, the
// AppError → HTTP status mapper, and small JSON-writing helpers.
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
)

// statusFor maps a domain error to (status code, machine-readable code,
// public message). Storage errors are deliberately scrubbed so callers
// don't get internal detail; the original is logged separately.
func statusFor(err error) (int, gen.ErrorCode, string) {
	var appErr *model.AppError
	if !errors.As(err, &appErr) {
		return http.StatusInternalServerError, gen.InternalError, "internal error"
	}
	switch appErr.Code {
	case model.CodeDuplicateUser:
		return http.StatusConflict, gen.DuplicateUser, appErr.Message
	case model.CodeInvalidUser:
		return http.StatusBadRequest, gen.InvalidUser, appErr.Message
	case model.CodeInvalidEmail:
		return http.StatusBadRequest, gen.InvalidEmail, appErr.Message
	case model.CodeInvalidPassword:
		return http.StatusBadRequest, gen.InvalidPassword, appErr.Message
	case model.CodeUserNotFound:
		return http.StatusNotFound, gen.UserNotFound, appErr.Message
	case model.CodeInvalidCredential:
		return http.StatusUnauthorized, gen.InvalidUser, appErr.Message
	case model.CodeStorage:
		return http.StatusInternalServerError, gen.StorageError, "storage error"
	default:
		return http.StatusInternalServerError, gen.InternalError, "internal error"
	}
}

// writeError serialises err into the spec's Error envelope at the
// mapped status. Internal errors are logged with the request context
// before being scrubbed for the response.
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, code, msg := statusFor(err)
	if status >= http.StatusInternalServerError {
		logger.Error("request failed",
			"method", r.Method, "path", r.URL.Path,
			"err", err.Error(),
		)
	}
	writeJSON(w, status, gen.Error{Code: code, Message: msg})
}
