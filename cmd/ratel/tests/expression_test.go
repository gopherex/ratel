//go:build integration

package tests_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/objectid"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// TestIndexExpressionCalls checks that the catalog binds each call of an
// index expression to the function PostgreSQL binds it to (the index's
// expression tree), for operator arguments, nested calls and untyped literals.
func TestIndexExpressionCalls(t *testing.T) {
	t.Parallel()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		t.Fatal(err)
	}

	const ddl = `CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE TABLE packs (id bigint PRIMARY KEY, title text, slug text, note varchar(20), n int4, m int8,
  deleted_at timestamptz);
CREATE INDEX packs_search ON packs USING gin (lower(title || ' ' || slug) gin_trgm_ops) WHERE deleted_at IS NULL;
CREATE INDEX packs_nested ON packs (lower(btrim(note)));
CREATE INDEX packs_sum ON packs (abs(n + m));
CREATE INDEX packs_literal ON packs (length(title || 'x'));`

	want := map[string][]string{
		"packs_search":  {"lower(text)"},
		"packs_nested":  {"btrim(text)", "lower(text)"},
		"packs_sum":     {"abs(bigint)"},
		"packs_literal": {"length(text)"},
	}

	for _, major := range []int{16, 17, 18, 19} {
		t.Run(strconv.Itoa(major), func(t *testing.T) {
			t.Parallel()

			signatures, err := builtins.For(major)
			if err != nil {
				t.Fatal(err)
			}

			built, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: ddl}}, major, signatures)
			if err != nil {
				t.Fatal(err)
			}

			pool := postgresPool(t, major)

			ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
			defer cancel()

			if _, err := pool.Exec(ctx, ddl); err != nil {
				t.Fatal(err)
			}

			for name, functions := range want {
				checkIndexCalls(ctx, t, pool, built, name, functions)
			}
		})
	}
}

// checkIndexCalls compares the functions PostgreSQL binds in the named
// index with the targets ratel chose. pg_depend omits pinned pg_catalog
// functions, so the bound ones come from the index's expression tree.
func checkIndexCalls(
	ctx context.Context, t *testing.T, pool *pgxpool.Pool, built *ir.Catalog, name string, functions []string,
) {
	t.Helper()

	const bound = `SELECT array_agg(DISTINCT m[1]::oid::regprocedure::text
   ORDER BY m[1]::oid::regprocedure::text)
 FROM pg_index, regexp_matches(indexprs::text, ':funcid (\d+)', 'g') AS m
 WHERE indexrelid = $1::regclass`

	var got []string
	if err := pool.QueryRow(ctx, bound, name).Scan(&got); err != nil {
		t.Fatal(err)
	}

	if strings.Join(got, ",") != strings.Join(functions, ",") {
		t.Fatalf("%s: PostgreSQL binds %v, want %v", name, got, functions)
	}

	targets := indexTargets(built, name)

	for _, function := range functions {
		target := "pg_catalog." + strings.Replace(function, "bigint", "int8", 1)
		if !targets[target] {
			t.Fatalf("%s: ratel binds %v, PostgreSQL %s", name, targets, target)
		}
	}
}

// indexTargets are the function targets of the calls in the named index.
func indexTargets(built *ir.Catalog, name string) map[string]bool {
	targets := make(map[string]bool)

	for _, schema := range built.GetSchemas() {
		for _, table := range schema.GetTables() {
			for _, index := range table.GetIndexes() {
				if index.GetName() == name {
					collectTargets(index.ProtoReflect(), targets)
				}
			}
		}
	}

	return targets
}

func collectTargets(message protoreflect.Message, targets map[string]bool) {
	if call, ok := message.Interface().(*ir.FunctionCall); ok && call.GetTarget() != nil {
		targets[objectid.ID(call.GetTarget())] = true
	}

	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.Message() == nil || field.IsMap():
		case field.IsList():
			for i := range value.List().Len() {
				collectTargets(value.List().Get(i).Message(), targets)
			}
		default:
			collectTargets(value.Message(), targets)
		}

		return true
	})
}
