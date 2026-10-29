//go:build integration

package bench_test

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxLibrary is the baseline: hand-written SQL and scanning on pgx.
func pgxLibrary(conn connections) library {
	pool := conn.pool

	return library{
		name: "pgx",
		one: func(ctx context.Context, id int64) (string, error) {
			var user pgxUser

			err := pool.QueryRow(ctx, `SELECT id, email, name, created_at FROM users WHERE id = $1`, id).
				Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt)

			return user.Email, err
		},
		list: func(ctx context.Context, after int64) ([]string, error) {
			rows, err := pool.Query(ctx, `SELECT id, email, name, created_at FROM users WHERE id > $1 ORDER BY id LIMIT $2`,
				after, pageSize)
			if err != nil {
				return nil, fmt.Errorf("list: %w", err)
			}

			users, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pgxUser])
			if err != nil {
				return nil, fmt.Errorf("list: %w", err)
			}

			emails := make([]string, 0, len(users))
			for _, user := range users {
				emails = append(emails, user.Email)
			}

			return emails, nil
		},
		insert: func(ctx context.Context, user newUser) error {
			_, err := pool.Exec(ctx, `INSERT INTO users (email, name) VALUES ($1, $2)`, user.email, user.name)

			return err
		},
		insertBatch: func(ctx context.Context, users []newUser) error {
			_, err := pool.CopyFrom(ctx, pgx.Identifier{"users"}, []string{"email", "name"},
				pgx.CopyFromSlice(len(users), func(index int) ([]any, error) {
					return []any{users[index].email, users[index].name}, nil
				}))

			return err
		},
		relations: func(ctx context.Context) (int, error) { return pgxRelations(ctx, pool) },
		update: func(ctx context.Context, id int64, name string) error {
			_, err := pool.Exec(ctx, `UPDATE users SET name = $2 WHERE id = $1`, id, name)

			return err
		},
	}
}

type pgxUser struct {
	ID        int64
	Email     string
	Name      string
	CreatedAt time.Time
	Orders    []pgxOrder `db:"-"`
}

type pgxOrder struct {
	ID     int64
	UserID int64
	Total  int64
	Status string
}

// pgxRelations reads a page of users, then their orders in one query, and
// attaches each order to its user.
func pgxRelations(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	rows, err := pool.Query(ctx, `SELECT id, email, name, created_at FROM users WHERE id > 0 ORDER BY id LIMIT $1`,
		pageSize)
	if err != nil {
		return 0, fmt.Errorf("users: %w", err)
	}

	users, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pgxUser])
	if err != nil {
		return 0, fmt.Errorf("users: %w", err)
	}

	ids := make([]int64, len(users))
	byID := make(map[int64]*pgxUser, len(users))

	for index := range users {
		ids[index] = users[index].ID
		byID[users[index].ID] = &users[index]
	}

	rows, err = pool.Query(ctx,
		`SELECT id, user_id, total, status FROM orders WHERE user_id = ANY($1) ORDER BY user_id, id`, ids)
	if err != nil {
		return 0, fmt.Errorf("orders: %w", err)
	}

	orders, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pgxOrder])
	if err != nil {
		return 0, fmt.Errorf("orders: %w", err)
	}

	for _, order := range orders {
		user := byID[order.UserID]
		user.Orders = append(user.Orders, order)
	}

	return len(orders), nil
}
