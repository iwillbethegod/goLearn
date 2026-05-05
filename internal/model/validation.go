package model

import "regexp"

// EmailRegex is the basic shape email addresses must match. It is
// deliberately permissive — RFC-5322 has more edge cases than any
// regex sanely encodes — and is meant as a sanity check, not a
// definitive test. Callers should also let downstream services (an
// SMTP provider, an email-verification flow) be the final authority.
const EmailRegex = `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`

var emailValidator = regexp.MustCompile(EmailRegex)

// IsValidEmail returns true when email matches EmailRegex.
func IsValidEmail(email string) bool {
	return emailValidator.MatchString(email)
}
