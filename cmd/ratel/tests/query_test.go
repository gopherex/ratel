//go:build integration

package tests_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/query"
)

const startupTimeout = 90 * time.Second

func TestQuerySignatures(t *testing.T) {
	t.Parallel()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		t.Fatal(err)
	}

	ddl, err := os.ReadFile("testdata/query/schema.sql")
	if err != nil {
		t.Fatal(err)
	}

	sql, err := os.ReadFile("testdata/query/queries.sql")
	if err != nil {
		t.Fatal(err)
	}

	sources, err := annotate.Queries(parser, []annotate.File{{Name: "queries.sql", SQL: string(sql)}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, major := range []int{16, 17, 18} {
		t.Run(strconv.Itoa(major), func(t *testing.T) {
			t.Parallel()

			signatures, loadErr := builtins.For(major)
			if loadErr != nil {
				t.Fatal(loadErr)
			}

			cat, buildErr := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: string(ddl)}}, major, signatures)
			if buildErr != nil {
				t.Fatal(buildErr)
			}

			inputs := versionQueries(t, parser, major, sources.Queries)

			inferred, inferErr := query.Infer(parser, cat, signatures, inputs)
			if inferErr != nil {
				t.Fatal(inferErr)
			}

			pool := postgresPool(t, major)

			ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
			defer cancel()

			if _, execErr := pool.Exec(ctx, string(ddl)); execErr != nil {
				t.Fatal(execErr)
			}

			if _, execErr := pool.Exec(ctx, `INSERT INTO public.users VALUES(1,'one',NULL,10),(2,'two',1,20)`); execErr != nil {
				t.Fatal(execErr)
			}

			if _, err := pool.Exec(ctx, `INSERT INTO public.money VALUES(1)`); err != nil {
				t.Fatal(err)
			}

			checkDomain(ctx, t, pool)
			checkRejected(t, pool, parser, cat, signatures)

			for _, item := range inferred {
				t.Run(item.GetName(), func(t *testing.T) {
					t.Parallel()
					checkQuery(t, pool, item)
				})
			}
		})
	}
}

func checkRejected(t *testing.T, pool *pgxpool.Pool, parser parse.Parser, cat *ir.Catalog, signatures []*ir.Schema) {
	t.Helper()

	for _, tc := range []struct{ name, sql, code string }{
		{"UnknownFunction", `SELECT public.missing(1)`, "42883"},
		{"AmbiguousDefaults", `SELECT public.tricky(1)`, "42725"},
		{"AmbiguousCategories", `SELECT public.choose_kind($1)`, "42725"},
		{"UnresolvedOccurrence", `SELECT $1 IS NULL FROM public.users WHERE id=$1`, "42P08"},
		{"InconsistentTarget", `SELECT $1 AS id FROM public.users WHERE id=$1`, "42P08"},
		{"InconsistentInsert", `INSERT INTO public.users(id,name,score)
 SELECT $1,$2,$3 FROM public.users u WHERE u.score=$1`, "42P08"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := query.Infer(parser, cat, signatures, []*annotate.QuerySource{{Name: tc.name, SQL: tc.sql}})

			var refusal *query.Error

			if !errors.As(err, &refusal) {
				t.Fatalf("expected inference refusal, got %v", err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
			defer cancel()

			connection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}

			defer connection.Release()
			_, err = connection.Conn().Prepare(ctx, tc.name, tc.sql)

			var server *pgconn.PgError

			if !errors.As(err, &server) || server.Code != tc.code {
				t.Fatalf("expected PostgreSQL %s, got %v", tc.code, err)
			}
		})
	}
}

func versionQueries(
	t *testing.T, parser parse.Parser, major int, sources []*annotate.QuerySource,
) []*annotate.QuerySource {
	t.Helper()

	const mergeReturningMajor = 17
	if major < mergeReturningMajor {
		return sources
	}

	sql, err := os.ReadFile("testdata/query/merge.sql")
	if err != nil {
		t.Fatal(err)
	}

	result, err := annotate.Queries(parser, []annotate.File{{Name: "merge.sql", SQL: string(sql)}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	return append(slices.Clone(sources), result.Queries...)
}

func postgresPool(t *testing.T, major int) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()

	container, err := postgres.Run(ctx, "postgres:"+strconv.Itoa(major)+"-alpine", testcontainers.WithWaitStrategy(
		wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(startupTimeout)))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), startupTimeout)
		defer cleanupCancel()

		if err := container.Terminate(cleanup); err != nil {
			t.Error(err)
		}
	})

	address, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, address)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	return pool
}

func checkQuery(t *testing.T, pool *pgxpool.Pool, item *ir.Query) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := transaction.Rollback(ctx); err != nil {
			t.Error(err)
		}
	}()

	description, err := transaction.Prepare(ctx, item.GetName(), item.GetSql())
	if err != nil {
		t.Fatal(err)
	}

	if len(description.ParamOIDs) != len(item.GetParameters()) || len(description.Fields) != len(item.GetColumns()) {
		t.Fatalf("signature widths: PostgreSQL params=%v columns=%v; inferred %v / %v",
			description.ParamOIDs, description.Fields, item.GetParameters(), item.GetColumns())
	}

	for index, oid := range description.ParamOIDs {
		checkType(ctx, t, transaction, oid, item.GetParameters()[index].GetType())
	}

	for index, field := range description.Fields {
		checkColumnType(ctx, t, transaction, field.DataTypeOID, item.GetColumns()[index].GetType())

		if field.Name != item.GetColumns()[index].GetName() {
			t.Errorf("column %d name: PostgreSQL=%s inferred=%s", index, field.Name, item.GetColumns()[index].GetName())
		}
	}

	if len(item.GetParameters()) == 0 && len(item.GetColumns()) > 0 {
		checkNullRows(ctx, t, transaction, item)
	}
}

func checkDomain(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	const sql = `SELECT pg_typeof(coalesce(value,NULL))::oid,pg_typeof(coalesce(value,value))::oid,
 'int4'::regtype::oid,'public.amount'::regtype::oid FROM public.money LIMIT 1`

	var mixed, uniform, base, domain uint32
	if err := pool.QueryRow(ctx, sql).Scan(&mixed, &uniform, &base, &domain); err != nil {
		t.Fatal(err)
	}

	if mixed != base || uniform != domain {
		t.Fatalf("domain common types: mixed=%d uniform=%d; want %d/%d", mixed, uniform, base, domain)
	}
}

func checkColumnType(ctx context.Context, t *testing.T, transaction pgx.Tx, oid uint32, inferred *ir.TypeRef) {
	t.Helper()

	if inferred.GetKind() != ir.TypeKind_TYPE_KIND_DOMAIN {
		checkType(ctx, t, transaction, oid, inferred)

		return
	}
	// RowDescription sends a domain's base OID; pg_typeof below checks the nominal result separately.
	const sql = `WITH RECURSIVE chain AS (
 SELECT oid,typbasetype FROM pg_catalog.pg_type WHERE oid=pg_catalog.to_regtype($1)
 UNION ALL SELECT t.oid,t.typbasetype FROM pg_catalog.pg_type t JOIN chain c ON t.oid=c.typbasetype
 ) SELECT oid FROM chain WHERE typbasetype=0`

	var base uint32
	if err := transaction.QueryRow(ctx, sql, catalog.ObjectID(inferred.GetUdt())).Scan(&base); err != nil {
		t.Fatal(err)
	}

	if base != oid {
		t.Errorf("PostgreSQL result OID=%d inferred domain base=%d", oid, base)
	}
}

func checkType(ctx context.Context, t *testing.T, transaction pgx.Tx, oid uint32, inferred *ir.TypeRef) {
	t.Helper()

	var name string

	row := transaction.QueryRow(ctx, `SELECT typname FROM pg_catalog.pg_type WHERE oid=$1`, oid)
	if err := row.Scan(&name); err != nil {
		t.Fatal(err)
	}

	if name != inferred.GetPgName() {
		t.Errorf("PostgreSQL type=%s inferred=%s", name, inferred.GetPgName())
	}
}

func checkNullRows(ctx context.Context, t *testing.T, transaction pgx.Tx, item *ir.Query) {
	t.Helper()

	rows, err := transaction.Query(ctx, item.GetSql())
	if err != nil {
		t.Fatal(err)
	}

	defer rows.Close()

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}

		for index, value := range values {
			if value == nil && !item.GetColumns()[index].GetNullable() {
				t.Errorf("column %d (%s) produced NULL but inferred NOT NULL", index, item.GetColumns()[index].GetName())
			}
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
