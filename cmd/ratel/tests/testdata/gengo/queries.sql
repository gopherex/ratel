-- name: UserByEmail :one
SELECT * FROM app.users WHERE email = @email;

-- name: OrdersWithUsers :many
SELECT o.*, u.*
FROM app.orders o LEFT JOIN app.users u ON u.id = o.user_id AND u.status = 'active'
ORDER BY o.id;

-- name: Emails :many
SELECT id, email FROM app.users;

-- name: CreateTag :one
INSERT INTO app.tags (id, label) VALUES (@id, @label) RETURNING *;

-- name: DeleteTag :execrows
DELETE FROM app.tags WHERE id = @id;

-- name: CopyTags :copyfrom
INSERT INTO app.tags (id, label) VALUES (@id, @label);
