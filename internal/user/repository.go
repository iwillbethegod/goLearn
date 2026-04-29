package user

import "regexp"

type ErrorCode string

const (
	CodeDuplicateUser ErrorCode = "duplicate_user"
	CodeInvalidUser   ErrorCode = "invalid_user"
	CodeInvalidEmail  ErrorCode = "invalid_email"
	CodeUserNotFound  ErrorCode = "user_not_found"
	CodeStorage       ErrorCode = "storage_error"
)

// EmailRegex pattern for basic email validation
const EmailRegex = `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`

var emailValidator = regexp.MustCompile(EmailRegex)

// IsValidEmail checks if email matches the basic regex pattern
func IsValidEmail(email string) bool {
	return emailValidator.MatchString(email)
}

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

func NewStorageError(err error) error {
	return &AppError{
		Code:    CodeStorage,
		Message: "storage operation failed",
		Err:     err,
	}
}

var (
	ErrDuplicateUser = &AppError{Code: CodeDuplicateUser, Message: "user already exists"}
	ErrInvalidUser   = &AppError{Code: CodeInvalidUser, Message: "invalid user data"}
	ErrInvalidEmail  = &AppError{Code: CodeInvalidEmail, Message: "invalid email format"}
	ErrUserNotFound  = &AppError{Code: CodeUserNotFound, Message: "user not found"}
)

type Repository interface {
	Add(User) error
	Remove(userID string) error
	List() ([]User, error)
}
