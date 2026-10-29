-- name: CustomerByEmail :one
SELECT * FROM shop.customers WHERE email = @email;
