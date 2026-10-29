//go:build integration

package bench_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type gormUser struct {
	ID        int64
	Email     string
	Name      string
	CreatedAt time.Time
	Orders    []gormOrder `gorm:"foreignKey:UserID"`
}

func (gormUser) TableName() string { return "users" }

type gormOrder struct {
	ID     int64
	UserID int64
	Total  int64
	Status string
}

func (gormOrder) TableName() string { return "orders" }

// gormLibrary is GORM over pgx's stdlib driver with the settings its
// performance guide recommends: no default transaction, prepared statements,
// no logging.
func gormLibrary(tb testing.TB, conn connections) library {
	tb.Helper()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: conn.db}), &gorm.Config{
		SkipDefaultTransaction: true, PrepareStmt: true, Logger: logger.Discard,
	})
	if err != nil {
		tb.Fatal(err)
	}

	return library{
		name: "gorm",
		one: func(ctx context.Context, id int64) (string, error) {
			var user gormUser

			err := gormDB.WithContext(ctx).First(&user, id).Error

			return user.Email, err
		},
		list: func(ctx context.Context, after int64) ([]string, error) {
			var users []gormUser
			if err := gormDB.WithContext(ctx).Where("id > ?", after).Order("id").Limit(pageSize).Find(&users).Error; err != nil {
				return nil, err
			}

			emails := make([]string, 0, len(users))
			for index := range users {
				emails = append(emails, users[index].Email)
			}

			return emails, nil
		},
		insert: func(ctx context.Context, user newUser) error {
			return gormDB.WithContext(ctx).Create(&gormUser{Email: user.email, Name: user.name}).Error
		},
		insertBatch: func(ctx context.Context, users []newUser) error {
			rows := make([]gormUser, len(users))
			for index, user := range users {
				rows[index] = gormUser{Email: user.email, Name: user.name}
			}

			return gormDB.WithContext(ctx).CreateInBatches(rows, len(rows)).Error
		},
		relations: func(ctx context.Context) (int, error) {
			var users []gormUser
			if err := gormDB.WithContext(ctx).Preload("Orders").Where("id > ?", 0).Order("id").Limit(pageSize).
				Find(&users).Error; err != nil {
				return 0, err
			}

			count := 0
			for index := range users {
				count += len(users[index].Orders)
			}

			return count, nil
		},
		update: func(ctx context.Context, id int64, name string) error {
			return gormDB.WithContext(ctx).Model(&gormUser{ID: id}).Update("name", name).Error
		},
	}
}
