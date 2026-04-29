# Email Validation & User Removal Features

This document describes the email validation and user removal features added to goLearn.

## Email Validation

All user additions now validate the email format using a regex pattern that matches basic RFC 5322 standards.

### Validation Rules

The email regex pattern: `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`

Valid emails:
- `alice@example.com`
- `bob.smith@company.co.uk`
- `test.user+tag@domain.io`

Invalid emails (rejected):
- `invalid` (missing @ and domain)
- `invalid-email` (missing @ and domain)
- `bob@` (missing domain)
- `bob@domain` (missing TLD)
- `@domain.com` (missing local part)

### Usage

The validation happens automatically when adding a user:

```bash
go run ./cmd/server --add
Enter user name: Alice
Enter user email: invalid-email
Error: invalid email format
```

## User Removal

Users can be removed from storage by their ID with confirmation.

### Interactive Remove

```bash
go run ./cmd/server --remove
```

This displays all users and prompts for the ID to remove:

```
Available users:
  u-1777468308818780000: Alice (alice@example.com)
  u-1777468872191449000: Bob (bob@example.com)

Enter user ID to remove: u-1777468308818780000
Are you sure? (yes/no): yes
✓ Removed user with id u-1777468308818780000
```

### Implementation

- **Memory Storage**: In-memory maps are updated and the operation is immediate
- **JSON File Storage**: File is persisted after removal; rollback occurs on save failure
- **Postgres Storage**: Currently a stub, ready for implementation

## Error Handling

New error codes for better error reporting:

```go
CodeInvalidEmail ErrorCode = "invalid_email"
CodeUserNotFound ErrorCode = "user_not_found"
```

These errors are logged with appropriate context:

```
level=ERROR msg="invalid email format" error="invalid email format" email=invalid-email
level=ERROR msg="repository failed to remove user" error="user not found" user_id=u-nonexistent
```

## Testing

Comprehensive tests verify:

1. **Email Validation**
   - Accepts valid email formats
   - Rejects invalid email formats
   - Handles edge cases (missing @, missing TLD, etc.)

2. **User Removal**
   - Successfully removes existing users
   - Errors when removing non-existent users
   - Persists removal to storage

Run tests:
```bash
go test ./... -v
```

## Architecture

### Repository Interface Extension

```go
type Repository interface {
    Add(User) error
    Remove(userID string) error
    List() ([]User, error)
}
```

### Service Method

```go
func (s *Service) RemoveUser(ctx context.Context, userID string) error
```

### Email Validation Function

```go
func IsValidEmail(email string) bool
```

## Future Enhancements

- Support for more email formats (DNS validation, SMTP verification)
- Soft deletes (mark users as deleted rather than removing)
- Batch remove operations
- User update/modify operations
