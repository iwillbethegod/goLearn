// Package model holds the project's data-bearing types — entities,
// errors, validation helpers, and DTO-shaped structs that flow
// between layers. Behavior-rich types (Service, repositories, Pool,
// Runner, Handler, …) live in their own feature packages and import
// from here.
//
// In Go, methods must be defined in the same package as their
// receiver type, so types with non-trivial method sets stay where
// they are. This package is intentionally limited to types whose
// methods are either none or trivially data-bound (e.g. AppError's
// Error()/Unwrap()/Is()).
package model

// User is the persistent profile record. PasswordHash is a bcrypt
// hash; an empty value means the user was created without credentials
// (e.g. via the legacy cmd/server -add flow before auth was added).
type User struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	PasswordHash string `json:"password_hash,omitempty"`
}
