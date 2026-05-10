package model_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
)

func TestIsValidEmail(t *testing.T) {
	cases := []struct {
		email string
		want  bool
	}{
		{"a@b.co", true},
		{"first.last+tag@sub.domain.com", true},
		{"x@y.io", true},
		{"", false},
		{"no-at-sign", false},
		{"@no-local.com", false},
		{"local@", false},
		{"local@no-dot", false},
		{"local@x.c", false},     // single-letter TLD
		{"sp ace@x.com", false},  // space
	}
	for _, c := range cases {
		if got := model.IsValidEmail(c.email); got != c.want {
			t.Errorf("IsValidEmail(%q) = %v, want %v", c.email, got, c.want)
		}
	}
}

func TestAppError_ErrorString(t *testing.T) {
	bare := &model.AppError{Code: model.CodeInvalidUser, Message: "no good"}
	if got := bare.Error(); got != "no good" {
		t.Errorf("bare Error() = %q, want \"no good\"", got)
	}

	wrapped := &model.AppError{
		Code:    model.CodeStorage,
		Message: "db blew up",
		Err:     fmt.Errorf("connection refused"),
	}
	want := "db blew up: connection refused"
	if got := wrapped.Error(); got != want {
		t.Errorf("wrapped Error() = %q, want %q", got, want)
	}
}

func TestAppError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	app := &model.AppError{Code: model.CodeStorage, Message: "outer", Err: inner}
	if !errors.Is(app, inner) {
		t.Fatal("errors.Is should walk Unwrap chain")
	}
}

// errors.Is between two AppErrors compares Code, so a wrapped
// ErrDuplicateUser still satisfies errors.Is(err, ErrDuplicateUser).
func TestAppError_IsByCode(t *testing.T) {
	wrapped := &model.AppError{
		Code:    model.CodeDuplicateUser,
		Message: "fancy wrapper",
	}
	if !errors.Is(wrapped, model.ErrDuplicateUser) {
		t.Fatal("AppError.Is should match by Code")
	}
	// And mismatching codes must NOT match.
	if errors.Is(wrapped, model.ErrUserNotFound) {
		t.Fatal("AppError.Is must not match across different codes")
	}
}

func TestNewStorageError_WrapsAndCodes(t *testing.T) {
	inner := errors.New("disk full")
	got := model.NewStorageError(inner)
	if !errors.Is(got, model.NewStorageError(nil)) {
		// AppError.Is matches by Code, regardless of inner err.
		t.Fatal("NewStorageError must produce an AppError with CodeStorage")
	}
	if !errors.Is(got, inner) {
		t.Fatal("NewStorageError must wrap inner err")
	}
	app, ok := got.(*model.AppError)
	if !ok {
		t.Fatalf("type = %T, want *model.AppError", got)
	}
	if app.Code != model.CodeStorage {
		t.Fatalf("Code = %q, want %q", app.Code, model.CodeStorage)
	}
}

func TestSentinelsHaveExpectedCodes(t *testing.T) {
	cases := map[*model.AppError]model.ErrorCode{
		model.ErrDuplicateUser:     model.CodeDuplicateUser,
		model.ErrInvalidUser:       model.CodeInvalidUser,
		model.ErrInvalidEmail:      model.CodeInvalidEmail,
		model.ErrInvalidPassword:   model.CodeInvalidPassword,
		model.ErrInvalidCredential: model.CodeInvalidCredential,
		model.ErrUserNotFound:      model.CodeUserNotFound,
	}
	for sentinel, code := range cases {
		if sentinel.Code != code {
			t.Errorf("%v Code = %q, want %q", sentinel, sentinel.Code, code)
		}
	}
}
