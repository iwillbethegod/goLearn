package user

// User is the persistent profile record. PasswordHash is a bcrypt
// hash; an empty value means the user was created without credentials
// (e.g. via the legacy cmd/server -add flow before auth was added).
type User struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	PasswordHash string `json:"password_hash,omitempty"`
}
