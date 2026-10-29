package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/pkg/ratel"
)

// level is a Go value stored as its decimal text, as a proto enum is stored
// as its name.
type level int

func levelCodec() ratel.Codec[level] {
	return ratel.Codec[level]{
		Encode: func(value level) any { return strconv.Itoa(int(value)) },
		Scan: func(rows pgx.Rows) (ratel.Null[level], error) {
			var value *int64
			if err := rows.Scan(&value); err != nil {
				return ratel.Null[level]{}, err
			}

			if value == nil {
				return ratel.Null[level]{}, nil
			}

			return ratel.NullFrom(level(*value)), nil
		},
	}
}

func TestCodecCol(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	email := ratel.NewCodecCol[readUser]("email", levelCodec())
	manager := ratel.NewCodecNullCol[readUser]("manager_id", levelCodec())

	for _, tc := range []struct {
		name  string
		query interface {
			Build() (string, []any, error)
		}
		sql  string
		args []any
	}{
		{"eq", ratel.From(users).Where(email.Eq(3)), `WHERE "q"."email" = $1`, []any{"3"}},
		{"between", ratel.From(users).Where(email.Between(1, 2)), `"q"."email" BETWEEN $1 AND $2`, []any{"1", "2"}},
		{"in", ratel.From(users).Where(email.In(4, 5)), `"q"."email" IN ($1, $2)`, []any{"4", "5"}},
		{"nullable eq", ratel.From(users).Where(manager.Eq(6)), `"q"."manager_id" = $1`, []any{"6"}},
		{
			"set", users.Update().Set(email.To(7), manager.To(ratel.NullFrom(level(8)))).Where(users.ID.Eq(1)),
			`SET "email" = $1, "manager_id" = $2`,
			[]any{"7", "8", int64(1)},
		},
		{
			"set null", users.Update().Set(manager.To(ratel.Null[level]{})).Where(users.ID.Eq(1)),
			`SET "manager_id" = $1`,
			[]any{nil, int64(1)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sql, args, err := tc.query.Build()
			if err != nil || !strings.Contains(sql, tc.sql) || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("Build() = %s, %#v, %v", sql, args, err)
			}
		})
	}
}

func TestCodecPluck(t *testing.T) {
	t.Parallel()

	users := userDescriptor()
	email := ratel.NewCodecCol[readUser]("email", levelCodec())
	manager := ratel.NewCodecNullCol[readUser]("manager_id", levelCodec())
	five := int64(5)

	database := func(values ...any) *ratel.DB {
		return ratel.New(readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
			return &selXRows{values: values}, nil
		}})
	}

	values, err := database(&five).Run(ratel.From(users).Pluck(email)).List(t.Context())
	if err != nil || !reflect.DeepEqual(values, []level{5}) {
		t.Fatalf("Pluck() = %v, %v", values, err)
	}

	nullable, err := database(&five, (*int64)(nil)).Run(ratel.From(users).PluckNull(manager)).List(t.Context())
	if err != nil || !reflect.DeepEqual(nullable, []ratel.Null[level]{ratel.NullFrom(level(5)), {}}) {
		t.Fatalf("PluckNull() = %v, %v", nullable, err)
	}

	if _, err := database((*int64)(nil)).Run(ratel.From(users).Pluck(email)).List(t.Context()); !errors.Is(
		err, ratel.ErrUnexpectedNull) {
		t.Fatalf("Pluck of NULL = %v", err)
	}
}

func BenchmarkCodecCol(b *testing.B) {
	users := userDescriptor()
	email := ratel.NewCodecCol[readUser]("email", levelCodec())

	for b.Loop() {
		if _, _, err := ratel.From(users).Where(email.In(1, 2, 3)).Build(); err != nil {
			b.Fatal(err)
		}
	}
}
