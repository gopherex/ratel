package ratel_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/pkg/ratel"
)

func TestConditionGroups(t *testing.T) {
	t.Parallel()

	users := userDescriptor()

	for _, tc := range []struct {
		name      string
		condition ratel.Cond[readUser]
		suffix    string
	}{
		{"zero", ratel.Cond[readUser]{}, "TRUE"},
		{"not", users.ID.Eq(1).Not(), `NOT ("q"."id" = $1)`},
		{"and", users.ID.Eq(1).And(users.ID.Eq(2)), `("q"."id" = $1 AND "q"."id" = $2)`},
		{"or", users.ID.Eq(1).Or(users.ID.Eq(2)), `("q"."id" = $1 OR "q"."id" = $2)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := build(ratel.From(users).Where(tc.condition))
			if err != nil || !strings.HasSuffix(statement.SQL, " WHERE "+tc.suffix) {
				t.Fatalf("%+v: %v", statement, err)
			}
		})
	}
}

func TestJoinConditionGroups(t *testing.T) {
	t.Parallel()

	users := userDescriptor()

	for _, tc := range []struct {
		name      string
		condition ratel.Cond2[readUser, readUser]
		alias     string
		want      string
		invalid   bool
	}{
		{"zero", ratel.Cond2[readUser, readUser]{}, "left", "TRUE", false},
		{
			"or", users.ID.EqCol(users.ID).Or(users.ID.GtCol(users.ID)), "left",
			`("left"."id" = "right"."id" OR "left"."id" > "right"."id")`, false,
		},
		{"invalid alias", users.ID.EqCol(users.ID), "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := renderCond(tc.condition, tc.alias, "right")
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid alias accepted")
				}

				return
			}

			if err != nil || statement.SQL != tc.want {
				t.Fatalf("%+v: %v", statement, err)
			}
		})
	}
}

func BenchmarkJoinConditions(b *testing.B) {
	users := userDescriptor()

	b.ReportAllocs()

	for b.Loop() {
		_, err := renderCond(users.ID.EqCol(users.ManagerID).And(users.ID.GtCol(users.ID)).Not(), "left", "right")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConditions(b *testing.B) {
	users := userDescriptor()

	b.ReportAllocs()

	for b.Loop() {
		_, err := build(ratel.From(users).Where(users.ID.Gt(1).Or(users.ID.Lt(3)).Not()))
		if err != nil {
			b.Fatal(err)
		}
	}
}
