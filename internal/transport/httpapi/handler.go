package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"

	"github.com/ashishsinghbhadoria/goLearn/internal/user"
)

// Handler implements gen.ServerInterface against the day-1 user
// service. The handler is intentionally thin: it converts between
// transport DTOs and domain types, delegates to Service, and maps
// errors via writeError.
type Handler struct {
	svc    *user.Service
	logger *slog.Logger
}

func NewHandler(svc *user.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Default and ceiling for the limit query param. The same numbers
// live in the OpenAPI spec; if you change one, change the other.
const (
	defaultPageLimit = 100
	maxPageLimit     = 1000
)

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request, params gen.ListUsersParams) {
	limit, offset := paginationParams(params)
	users, total, err := h.svc.ListUsers(r.Context(), limit, offset)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	out := make([]gen.User, 0, len(users))
	for _, u := range users {
		out = append(out, toAPIUser(u))
	}
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSON(w, http.StatusOK, out)
}

func paginationParams(params gen.ListUsersParams) (limit, offset int) {
	limit = defaultPageLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	offset = 0
	if params.Offset != nil && *params.Offset >= 0 {
		offset = *params.Offset
	}
	return
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req gen.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, h.logger, &model.AppError{Code: model.CodeInvalidUser, Message: "invalid request body: " + err.Error()})
		return
	}
	created, err := h.svc.Register(r.Context(), req.Name, string(req.Email), req.Password)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	w.Header().Set("Location", "/users/"+created.ID)
	writeJSON(w, http.StatusCreated, toAPIUser(created))
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request, id gen.UserID) {
	u, err := h.svc.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIUser(u))
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request, id gen.UserID) {
	var req gen.UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, h.logger, &model.AppError{Code: model.CodeInvalidUser, Message: "invalid request body: " + err.Error()})
		return
	}
	var name, email string
	if req.Name != nil {
		name = *req.Name
	}
	if req.Email != nil {
		email = string(*req.Email)
	}
	updated, err := h.svc.UpdateUser(r.Context(), id, name, email)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toAPIUser(updated))
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request, id gen.UserID) {
	if err := h.svc.RemoveUser(r.Context(), id); err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toAPIUser strips PasswordHash and converts to the spec's User shape.
func toAPIUser(u model.User) gen.User {
	return gen.User{
		Id:    u.ID,
		Name:  u.Name,
		Email: openapi_types.Email(u.Email),
	}
}
