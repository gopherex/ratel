//go:build integration

package bench_test

import (
	"context"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

type bunUser struct {
	bun.BaseModel `bun:"table:users"`

	ID        int64      `bun:"id,pk,autoincrement"`
	Email     string     `bun:"email"`
	Name      string     `bun:"name"`
	CreatedAt time.Time  `bun:"created_at,nullzero,notnull,default:current_timestamp"`
	Orders    []bunOrder `bun:"rel:has-many,join:id=user_id"`
}

type bunOrder struct {
	bun.BaseModel `bun:"table:orders"`

	ID     int64  `bun:"id,pk,autoincrement"`
	UserID int64  `bun:"user_id"`
	Total  int64  `bun:"total"`
	Status string `bun:"status"`
}

// bunLibrary is bun over pgx's stdlib driver.
func bunLibrary(conn connections) library {
	bunDB := bun.NewDB(conn.db, pgdialect.New())

	return library{
		name: "bun",
		one: func(ctx context.Context, id int64) (string, error) {
			var user bunUser

			err := bunDB.NewSelect().Model(&user).Where("id = ?", id).Scan(ctx)

			return user.Email, err
		},
		list: func(ctx context.Context, after int64) ([]string, error) {
			var users []bunUser
			if err := bunDB.NewSelect().Model(&users).Where("id > ?", after).Order("id").Limit(pageSize).
				Scan(ctx); err != nil {
				return nil, err
			}

			emails := make([]string, 0, len(users))
			for index := range users {
				emails = append(emails, users[index].Email)
			}

			return emails, nil
		},
		insert: func(ctx context.Context, user newUser) error {
			_, err := bunDB.NewInsert().Model(&bunUser{Email: user.email, Name: user.name}).Exec(ctx)

			return err
		},
		insertBatch: func(ctx context.Context, users []newUser) error {
			rows := make([]bunUser, len(users))
			for index, user := range users {
				rows[index] = bunUser{Email: user.email, Name: user.name}
			}

			_, err := bunDB.NewInsert().Model(&rows).Exec(ctx)

			return err
		},
		relations: func(ctx context.Context) (int, error) {
			var users []bunUser
			if err := bunDB.NewSelect().Model(&users).Relation("Orders").Where("id > ?", 0).Order("id").
				Limit(pageSize).Scan(ctx); err != nil {
				return 0, err
			}

			count := 0
			for index := range users {
				count += len(users[index].Orders)
			}

			return count, nil
		},
		update: func(ctx context.Context, id int64, name string) error {
			_, err := bunDB.NewUpdate().Model((*bunUser)(nil)).Set("name = ?", name).Where("id = ?", id).Exec(ctx)

			return err
		},
	}
}
