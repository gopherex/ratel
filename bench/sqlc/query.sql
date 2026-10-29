-- name: User :one
SELECT * FROM users WHERE id = $1;

-- name: Users :many
SELECT * FROM users WHERE id > $1 ORDER BY id LIMIT $2;

-- name: OrdersOf :many
SELECT * FROM orders WHERE user_id = ANY($1::bigint[]) ORDER BY user_id, id;

-- name: CreateUser :one
INSERT INTO users (email, name) VALUES ($1, $2) RETURNING id;

-- name: CopyUsers :copyfrom
INSERT INTO users (email, name) VALUES ($1, $2);

-- name: RenameUser :exec
UPDATE users SET name = $2 WHERE id = $1;
