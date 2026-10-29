//go:build integration

package bench_test

import (
	"context"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/gopherex/ratel/bench/ent"
	"github.com/gopherex/ratel/bench/ent/user"
)

// entLibrary is ent's generated client over pgx's stdlib driver.
func entLibrary(conn connections) library {
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, conn.db)))

	return library{
		name: "ent",
		one: func(ctx context.Context, id int64) (string, error) {
			found, err := client.User.Get(ctx, id)
			if err != nil {
				return "", err
			}

			return found.Email, nil
		},
		list: func(ctx context.Context, after int64) ([]string, error) {
			return client.User.Query().Where(user.IDGT(after)).Order(ent.Asc(user.FieldID)).Limit(pageSize).
				Select(user.FieldEmail).Strings(ctx)
		},
		insert: func(ctx context.Context, row newUser) error {
			return client.User.Create().SetEmail(row.email).SetName(row.name).Exec(ctx)
		},
		insertBatch: func(ctx context.Context, rows []newUser) error {
			builders := make([]*ent.UserCreate, len(rows))
			for index, row := range rows {
				builders[index] = client.User.Create().SetEmail(row.email).SetName(row.name)
			}

			return client.User.CreateBulk(builders...).Exec(ctx)
		},
		relations: func(ctx context.Context) (int, error) {
			users, err := client.User.Query().Where(user.IDGT(0)).Order(ent.Asc(user.FieldID)).Limit(pageSize).
				WithOrders().All(ctx)
			if err != nil {
				return 0, err
			}

			count := 0
			for _, found := range users {
				count += len(found.Edges.Orders)
			}

			return count, nil
		},
		update: func(ctx context.Context, id int64, name string) error {
			return client.User.UpdateOneID(id).SetName(name).Exec(ctx)
		},
	}
}
