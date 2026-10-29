package ratel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/pkg/ratel"
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
		{Column: "user_id", Parent: "id", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(
			models []relationUser,
		) any {
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

// relCapture loads one parent through the relation and returns the first queued
// statement with its arguments.
func relCapture[M any](t *testing.T, relation ratel.Relation[M]) (pgx.QueuedQuery, error) {
	t.Helper()

	var queued pgx.QueuedQuery

	executor := relationExecutor{send: func(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
		queued = *batch.QueuedQueries[0]

		return &relationBatch{rows: []*relationRows{{}}}
	}}
	err := ratel.New(executor).Load(t.Context(), make([]M, 1), relation)

	return queued, err
}

func TestRelOrderBy(t *testing.T) {
	t.Parallel()

	byID := ratel.NewCol[relationOrder, int64]("id")
	owner := ratel.NewCol[relationOrder, int64]("user_id")

	for _, tc := range []struct {
		name      string
		build     func(ratel.Rel[relationUser, relationOrder]) ratel.Rel[relationUser, relationOrder]
		want      string
		forbidden string
		invalid   bool
	}{
		{
			name: "single",
			build: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Rel[relationUser, relationOrder] {
				return rel.OrderBy(byID.Desc())
			},
			want: `ORDER BY "q"."id" DESC`, forbidden: `ORDER BY "p"`,
		},
		{
			name: "several terms",
			build: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Rel[relationUser, relationOrder] {
				return rel.OrderBy(byID.Asc(), owner.Desc())
			},
			want: `ORDER BY "q"."id" ASC, "q"."user_id" DESC`,
		},
		{
			name: "calls append",
			build: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Rel[relationUser, relationOrder] {
				return rel.OrderBy(byID.Asc()).OrderBy(owner.Desc())
			},
			want: `"q"."id" ASC, "q"."user_id" DESC`,
		},
		{
			name: "no terms",
			build: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Rel[relationUser, relationOrder] {
				return rel.OrderBy()
			},
			want:      `ANY($1)`,
			forbidden: `ORDER BY`,
		},
		{
			name: "invalid column",
			build: func(rel ratel.Rel[relationUser, relationOrder]) ratel.Rel[relationUser, relationOrder] {
				return rel.OrderBy(ratel.NewCol[relationOrder, int64]("").Asc())
			},
			invalid: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			users, _ := relationDescriptors()
			queued, err := relCapture[relationUser](t, tc.build(users.Orders))
			statement := queued.SQL

			if tc.invalid {
				if !errors.Is(err, ratel.ErrInvalidQuery) || statement != "" {
					t.Fatalf("statement=%q err=%v", statement, err)
				}

				return
			}

			if err != nil || !strings.Contains(statement, tc.want) ||
				(tc.forbidden != "" && strings.Contains(statement, tc.forbidden)) {
				t.Fatalf("want %q forbidden %q: %v\n%s", tc.want, tc.forbidden, err, statement)
			}
		})
	}
}

func TestRelOrderByKeepsOriginal(t *testing.T) {
	t.Parallel()

	users, _ := relationDescriptors()
	byID := ratel.NewCol[relationOrder, int64]("id")
	ordered := users.Orders.OrderBy(byID.Desc())

	queued, err := relCapture[relationUser](t, users.Orders)
	if err != nil || strings.Contains(queued.SQL, "DESC") {
		t.Fatalf("original relation mutated: %v\n%s", err, queued.SQL)
	}

	queued, err = relCapture[relationUser](t, ordered)
	if err != nil || !strings.Contains(queued.SQL, `"q"."id" DESC`) {
		t.Fatalf("ordered relation lost its order: %v\n%s", err, queued.SQL)
	}
}

func BenchmarkRelOrderBy(b *testing.B) {
	users, _ := relationDescriptors()
	byID := ratel.NewCol[relationOrder, int64]("id")
	owner := ratel.NewCol[relationOrder, int64]("user_id")

	b.ReportAllocs()

	for b.Loop() {
		_ = users.Orders.OrderBy(byID.Desc(), owner.Asc())
	}
}
