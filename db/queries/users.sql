-- name: AddUser :exec
INSERT INTO users (id, name, email, password_hash)
VALUES ($1, $2, $3, $4);

-- name: LogRegistration :exec
INSERT INTO registration_log (user_id, event)
VALUES ($1, $2);

-- name: GetUserByID :one
SELECT id, name, email, password_hash
FROM   users
WHERE  id = $1;

-- name: GetUserByEmail :one
SELECT id, name, email, password_hash
FROM   users
WHERE  lower(email) = lower($1);

-- name: ListUsers :many
SELECT id, name, email, password_hash
FROM   users
ORDER  BY id
LIMIT  $1 OFFSET $2;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: UpdateUser :one
UPDATE users
SET    name = sqlc.arg(name),
       email = sqlc.arg(email),
       password_hash = COALESCE(NULLIF(sqlc.arg(password_hash)::text, ''), password_hash)
WHERE  id = sqlc.arg(id)
RETURNING id, name, email, password_hash;

-- name: DeleteUser :one
DELETE FROM users
WHERE  id = $1
RETURNING id;
