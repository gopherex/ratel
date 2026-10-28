-- name: UserByEmail :one
SELECT * FROM app.users WHERE email = @email;

-- name: UsersWithOrderCount :many
SELECT u.*, count(o.id) AS order_count
FROM app.users u LEFT JOIN app.orders o ON o.user_id = u.id
GROUP BY u.id;

-- name: OrdersWithUsers :many
SELECT o.*, u.*
FROM app.orders o LEFT JOIN app.users u ON u.id = o.user_id;

-- name: OrderTotals :many
-- @ratel.row Total
SELECT user_id, sum(total) AS amount FROM app.orders GROUP BY user_id;

-- name: TopTotals :many
-- @ratel.row Total
SELECT user_id, max(total) AS amount FROM app.orders GROUP BY user_id;

-- name: EmailsLike :many
SELECT id, email FROM app.users WHERE email LIKE @pattern AND status = @status AND manager_id = @manager AND balance > @minimum;

-- name: EmailsOfManagers :many
SELECT id, email FROM app.users WHERE manager_id IS NULL;

-- name: CreateTag :one
INSERT INTO app.tags (id, label) VALUES (@id, @label) RETURNING *;

-- name: DeleteTag :exec
DELETE FROM app.tags WHERE id = @id;

-- name: DeleteOrders :execrows
DELETE FROM app.orders WHERE user_id = @user_id;

-- name: CopyTags :copyfrom
INSERT INTO app.tags (id, label) VALUES (@id, @label);
