package ratel_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

type relationUser struct {
	ID int64
	R  relationUserRelations
}

type relationUserRelations struct {
	Orders     ratel.Loaded[[]relationOrder]
	OrderCount ratel.Loaded[int64]
}

func (u *relationUser) Orders() ([]relationOrder, error) { return u.R.Orders.Get() }

type relationOrder struct {
	ID     int64
	UserID int64
	R      relationOrderRelations
}

type relationOrderRelations struct {
	Items     ratel.Loaded[[]relationOrder]
	ItemCount ratel.Loaded[int64]
}

type relationUsers struct {
	ratel.Table[relationUser]
	ID     ratel.Col[relationUser, int64]
	Orders ratel.Rel[relationUser, relationOrder]
}

func relationDescriptors() (relationUsers, ratel.Table[relationOrder]) {
	orders := ratel.NewTable("app", "orders", []string{"id", "user_id"},
		func(row pgx.Row) (relationOrder, error) {
			var model relationOrder
			err := row.Scan(&model.ID, &model.UserID)

			return model, err
		}, ratel.PrimaryKey([]string{"id"}, func(model relationOrder) []any { return []any{model.ID} }))
	users := relationUsers{
		Table: ratel.NewTable("app", "users", []string{"id"}, func(row pgx.Row) (relationUser, error) {
			var model relationUser
			err := row.Scan(&model.ID)

			return model, err
		}, ratel.PrimaryKey([]string{"id"}, func(model relationUser) []any { return []any{model.ID} })),
		ID: ratel.NewCol[relationUser, int64]("id"),
	}
	users.Orders = ratel.NewRel(orders, ratel.RelMany, []ratel.RelKey[relationUser]{
		{Column: "user_id", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(models []relationUser) any {
			values := make([]int64, len(models))
			for index, model := range models {
				values[index] = model.ID
			}

			return values
		}},
	}, func(model *relationUser, children []relationOrder) { model.R.Orders = ratel.LoadedFrom(children) },
		ratel.RelCount[relationUser, relationOrder](func(model *relationUser, count int64) {
			model.R.OrderCount = ratel.LoadedFrom(count)
		}))

	return users, orders
}

func TestLoaded(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		value  ratel.Loaded[[]int]
		loaded bool
		length int
	}{
		{name: "unrequested"},
		{name: "empty", value: ratel.LoadedFrom([]int{}), loaded: true},
		{name: "present", value: ratel.LoadedFrom([]int{1}), loaded: true, length: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			value, err := tc.value.Get()
			if tc.loaded {
				if err != nil || len(value) != tc.length || tc.value.IsZero() {
					t.Fatalf("value=%v err=%v", value, err)
				}
			} else if !errors.Is(err, ratel.ErrNotLoaded) || !tc.value.IsZero() {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func BenchmarkLoaded(b *testing.B) {
	value := ratel.LoadedFrom([]int{1})

	b.ReportAllocs()

	for b.Loop() {
		if _, err := value.Get(); err != nil {
			b.Fatal(err)
		}
	}
}
