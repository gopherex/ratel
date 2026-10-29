-- name: UserByEmail :one
SELECT * FROM app.users WHERE email = @email;

-- name: TopCustomers :many
SELECT u.email, sum(o.total) AS spent
FROM app.users u JOIN app.orders o ON o.user_id = u.id
GROUP BY u.email
ORDER BY spent DESC
LIMIT @limit;
