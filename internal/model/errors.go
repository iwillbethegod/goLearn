package model

// ErrorCode is the cross-cutting error vocabulary that flows from
// the domain layer all the way out to HTTP responses, CLI exit
// messages, and structured logs. Adding a new code here requires
// updating the spec's `Error.code` enum and the HTTP status mapper
// in transport/httpapi/errors.go.
type ErrorCode string

const (
	CodeDuplicateUser     ErrorCode = "duplicate_user"
	CodeInvalidUser       ErrorCode = "invalid_user"
	CodeInvalidEmail      ErrorCode = "invalid_email"
	CodeInvalidPassword   ErrorCode = "invalid_password"
	CodeInvalidCredential ErrorCode = "invalid_credential"
	CodeUserNotFound      ErrorCode = "user_not_found"
	CodeStorage           ErrorCode = "storage_error"
)

// AppError is a tagged error that lets callers branch on a stable
// Code without parsing the human-readable Message. errors.Is on two
// AppErrors compares the Code, so package-level sentinels like
// ErrDuplicateUser work even when wrapped.
type AppError struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func (e *AppError) Is(target error) bool {
	targetErr, ok := target.(*AppError)
	return ok && e.Code == targetErr.Code
}

// NewStorageError wraps a low-level storage failure (file IO, DB
// driver, etc.) so callers can detect it with errors.Is(err,
// ErrStorageError) without exposing the underlying type.
func NewStorageError(err error) error {
	return &AppError{
		Code:    CodeStorage,
		Message: "storage operation failed",
		Err:     err,
	}
}

// Sentinels — compare with errors.Is(err, model.ErrXxx). The Service
// returns ErrInvalidCredential for *any* auth failure (wrong email,
// no such user, wrong password) so callers cannot leak which factor
// was wrong.
var (
	ErrDuplicateUser     = &AppError{Code: CodeDuplicateUser, Message: "user already exists"}
	ErrInvalidUser       = &AppError{Code: CodeInvalidUser, Message: "invalid user data"}
	ErrInvalidEmail      = &AppError{Code: CodeInvalidEmail, Message: "invalid email format"}
	ErrInvalidPassword   = &AppError{Code: CodeInvalidPassword, Message: "password does not meet requirements"}
	ErrInvalidCredential = &AppError{Code: CodeInvalidCredential, Message: "invalid email or password"}
	ErrUserNotFound      = &AppError{Code: CodeUserNotFound, Message: "user not found"}
)
