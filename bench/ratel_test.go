//go:build integration

package bench_test

import (
	"context"
	"fmt"

	"github.com/gopherex/ratel/pkg/ratel"

	"github.com/gopherex/ratel/bench/internal/rateldb/public"
)

// ratelLibrary is ratel's generated descriptors and query builder on pgx.
func ratelLibrary(conn connections) library {
	database := ratel.New(conn.pool)

	return library{
		name: "ratel",
		one: func(ctx context.Context, id int64) (string, error) {
			user, err := database.Run(public.Users.Select().Where(public.Users.ID.Eq(id))).One(ctx)

			return user.Email, err
		},
		list: func(ctx context.Context, after int64) ([]string, error) {
			users, err := database.Run(public.Users.Select().Where(public.Users.ID.Gt(after)).
				OrderBy(public.Users.ID.Asc()).Limit(pageSize)).List(ctx)
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
			_, err := database.Run(public.Users.Insert(public.Users.Row().Email(user.email).Name(user.name))).Exec(ctx)

			return err
		},
		insertBatch: func(ctx context.Context, users []newUser) error {
			rows := make([]public.UserInsert, len(users))
			for index, user := range users {
				rows[index] = public.Users.Row().Email(user.email).Name(user.name)
			}

			_, err := database.Run(public.Users.Insert(rows...)).Exec(ctx)

			return err
		},
		relations: func(ctx context.Context) (int, error) {
			users, err := database.Run(public.Users.Select().Where(public.Users.ID.Gt(0)).OrderBy(public.Users.ID.Asc()).
				Limit(pageSize).With(public.Users.Orders)).List(ctx)
			if err != nil {
				return 0, fmt.Errorf("relations: %w", err)
			}

			count := 0

			for index := range users {
				orders, err := users[index].Orders()
				if err != nil {
					return 0, fmt.Errorf("relations: %w", err)
				}

				count += len(orders)
			}

			return count, nil
		},
		update: func(ctx context.Context, id int64, name string) error {
			_, err := database.Run(public.Users.Update().Set(public.Users.Name.To(name)).
				Where(public.Users.ID.Eq(id))).Exec(ctx)

			return err
		},
	}
}
