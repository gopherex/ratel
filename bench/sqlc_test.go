//go:build integration

package bench_test

import (
	"context"
	"fmt"

	"github.com/gopherex/ratel/bench/internal/sqlcdb"
)

// sqlcLibrary is sqlc's generated queries on pgx; relations are two queries
// stitched by hand, as sqlc leaves them to the caller.
func sqlcLibrary(conn connections) library {
	queries := sqlcdb.New(conn.pool)

	return library{
		name: "sqlc",
		one: func(ctx context.Context, id int64) (string, error) {
			user, err := queries.User(ctx, id)

			return user.Email, err
		},
		list: func(ctx context.Context, after int64) ([]string, error) {
			users, err := queries.Users(ctx, sqlcdb.UsersParams{ID: after, Limit: pageSize})
			if err != nil {
				return nil, fmt.Errorf("list: %w", err)
			}

			emails := make([]string, 0, len(users))
			for index := range users {
				emails = append(emails, users[index].Email)
			}

			return emails, nil
		},
		insert: func(ctx context.Context, user newUser) error {
			_, err := queries.CreateUser(ctx, sqlcdb.CreateUserParams{Email: user.email, Name: user.name})

			return err
		},
		insertBatch: func(ctx context.Context, users []newUser) error {
			rows := make([]sqlcdb.CopyUsersParams, len(users))
			for index, user := range users {
				rows[index] = sqlcdb.CopyUsersParams{Email: user.email, Name: user.name}
			}

			_, err := queries.CopyUsers(ctx, rows)

			return err
		},
		relations: func(ctx context.Context) (int, error) {
			users, err := queries.Users(ctx, sqlcdb.UsersParams{ID: 0, Limit: pageSize})
			if err != nil {
				return 0, fmt.Errorf("users: %w", err)
			}

			ids := make([]int64, len(users))
			for index := range users {
				ids[index] = users[index].ID
			}

			orders, err := queries.OrdersOf(ctx, ids)
			if err != nil {
				return 0, fmt.Errorf("orders: %w", err)
			}

			byUser := make(map[int64][]sqlcdb.Order, len(users))
			for _, order := range orders {
				byUser[order.UserID] = append(byUser[order.UserID], order)
			}

			return len(orders), nil
		},
		update: func(ctx context.Context, id int64, name string) error {
			return queries.RenameUser(ctx, sqlcdb.RenameUserParams{ID: id, Name: name})
		},
	}
}
