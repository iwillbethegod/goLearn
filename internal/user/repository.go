package user

type ErrorCode string

const (
	CodeDuplicateUser ErrorCode = "duplicate_user"
	CodeInvalidUser   ErrorCode = "invalid_user"
	CodeStorage       ErrorCode = "storage_error"
)

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
)

type Repository interface {
	Add(User) error
	List() ([]User, error)
}
