-- name: AddUser :exec
INSERT INTO users (id, name, email, password_hash)
VALUES ($1, $2, $3, $4);

-- name: LogRegistration :exec
INSERT INTO registration_log (user_id, event)
VALUES ($1, $2);

-- name: GetUserByID :one
SELECT id, name, email, password_hash, created_at
FROM   users
WHERE  id = $1;

-- name: GetUserByEmail :one
SELECT id, name, email, password_hash, created_at
FROM   users
WHERE  lower(email) = lower($1);

-- name: ListUsers :many
SELECT id, name, email, password_hash, created_at
FROM   users
ORDER  BY id;

-- name: UpdateUser :exec
UPDATE users
SET    name = $2,
       email = $3,
       password_hash = $4
WHERE  id = $1;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;
