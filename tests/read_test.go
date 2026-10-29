//go:build integration

package tests_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel/pkg/ratel"
)

type readAccount struct {
	Tenant    int64
	ID        int64
	Email     string
	ManagerID ratel.Null[int64]
}

type readAccounts struct {
	ratel.Table[readAccount]
	Tenant    ratel.Col[readAccount, int64]
	ID        ratel.Col[readAccount, int64]
	Email     ratel.Col[readAccount, string]
	ManagerID ratel.NullCol[readAccount, int64]
}

func accountDescriptor() readAccounts {
	return readAccounts{
		Table: ratel.NewTable("read_app", "accounts", []string{"tenant", "id", "email", "manager_id"}, scanAccount,
			ratel.PrimaryKey([]string{"tenant", "id"}, func(account readAccount) []any {
				return []any{account.Tenant, account.ID}
			})),
		Tenant:    ratel.NewCol[readAccount, int64]("tenant"),
		ID:        ratel.NewCol[readAccount, int64]("id"),
		Email:     ratel.NewCol[readAccount, string]("email"),
		ManagerID: ratel.NewNullCol[readAccount, int64]("manager_id"),
	}
}

func scanAccount(row pgx.Row) (readAccount, error) {
	var account readAccount

	err := row.Scan(&account.Tenant, &account.ID, &account.Email, &account.ManagerID)

	return account, err
}

func readPool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()

	pool := valuePool(tb)

	ctx, cancel := context.WithTimeout(tb.Context(), containerWait)
	defer cancel()

	_, err := pool.Exec(ctx, `CREATE SCHEMA read_app;
 CREATE TABLE read_app.accounts (
 tenant bigint NOT NULL, id bigint NOT NULL, email text NOT NULL,
 manager_id bigint, PRIMARY KEY (tenant, id));
 INSERT INTO read_app.accounts VALUES
 (1, 1, 'a@example.org', NULL), (1, 2, 'b@example.org', 1),
 (2, 1, 'c@acme.com', NULL), (2, 2, 'd@acme.com', 1), (2, 3, 'e@acme.com', 2);`)
	if err != nil {
		tb.Fatal(err)
	}

	return pool
}

func TestReadRuntime(t *testing.T) {
	t.Parallel()

	pool := readPool(t)

	for _, tc := range []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool)
	}{
		{"terminals", readTerminals},
		{"subqueries", readSubqueries},
		{"composite chunks", readChunks},
		{"early break", readEarlyBreak},
		{"transaction", readTransaction},
		{"cancellation", readCancellation},
		{"connection failure", readConnectionFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, pool)
		})
	}
}

func readTerminals(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	accounts := accountDescriptor()
	database := ratel.New(pool)

	for _, mode := range []pgx.QueryExecMode{
		pgx.QueryExecModeCacheStatement, pgx.QueryExecModeCacheDescribe,
		pgx.QueryExecModeExec, pgx.QueryExecModeSimpleProtocol,
	} {
		query := accounts.Select().Where(accounts.Tenant.Eq(1)).OrderBy(accounts.ID.Asc())

		users, err := database.Run(query).List(t.Context(), ratel.ExecMode(mode))
		if err != nil || len(users) != 2 || users[0].ManagerID.Valid || users[1].ManagerID.V != 1 {
			t.Fatalf("%v: %+v: %v", mode, users, err)
		}

		first, err := database.Run(query).First(t.Context(), ratel.ExecMode(mode))
		if err != nil || first.ID != 1 {
			t.Fatalf("%+v: %v", first, err)
		}

		one, err := database.Run(query.Where(accounts.ManagerID.IsNotNull())).One(t.Context(), ratel.ExecMode(mode))
		if err != nil || one.ID != 2 {
			t.Fatalf("%+v: %v", one, err)
		}

		_, err = database.Run(query).One(t.Context(), ratel.ExecMode(mode))
		if !errors.Is(err, ratel.ErrMultipleRows) {
			t.Fatalf("expected multiple rows: %v", err)
		}

		_, err = database.Run(query.Where(accounts.ID.Eq(99))).One(t.Context(), ratel.ExecMode(mode))
		if !errors.Is(err, ratel.ErrNotFound) {
			t.Fatalf("expected not found: %v", err)
		}
	}
}

func readSubqueries(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	accounts := accountDescriptor()
	database := ratel.New(pool)
	inner := accounts.Select().Where(accounts.Tenant.Eq(2)).OrderBy(accounts.ID.Asc()).Limit(2)
	outer := ratel.From(inner).Where(accounts.ID.Gt(1))

	rows, err := database.Run(ratel.From(outer).Where(accounts.Email.ILike("%@ACME.COM"))).List(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != 2 {
		t.Fatalf("%+v: %v", rows, err)
	}

	source := ratel.NewQuery([]string{"tenant", "id", "email", "manager_id"}, scanAccount,
		`SELECT tenant, id, email, manager_id FROM read_app.accounts WHERE tenant=$1 ORDER BY id LIMIT $2`, int64(2), 2)

	rows, err = database.Run(source.Select().Where(accounts.ID.Gt(1))).List(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != 2 {
		t.Fatalf("%+v: %v", rows, err)
	}
}

func readChunks(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	accounts := accountDescriptor()
	query := ratel.New(pool).Run(accounts.Select().OrderBy(accounts.Tenant.Asc(), accounts.ID.Asc()).Offset(1).Limit(3))

	var keys [][2]int64

	for page, err := range query.Chunks(t.Context(), 2) {
		if err != nil {
			t.Fatal(err)
		}

		for _, account := range page {
			keys = append(keys, [2]int64{account.Tenant, account.ID})
		}
	}

	if !reflect.DeepEqual(keys, [][2]int64{{1, 2}, {2, 1}, {2, 2}}) {
		t.Fatalf("keys: %v", keys)
	}
}

func readEarlyBreak(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	connection, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	defer connection.Release()

	accounts := accountDescriptor()
	database := ratel.New(connection.Conn())

	for _, err := range database.Run(accounts.Select()).All(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}

		break
	}

	// A leaked cursor makes this query fail with "conn busy" on the same connection.
	if _, err := database.Run(accounts.Select()).First(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, err := range database.Run(accounts.Select()).Chunks(t.Context(), 2) {
		if err != nil {
			t.Fatal(err)
		}

		if _, err := database.Run(accounts.Select()).First(t.Context()); err != nil {
			t.Fatal(err)
		}

		break
	}
}

func readTransaction(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	transaction, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	defer transaction.Rollback(t.Context())

	accounts := accountDescriptor()

	account, err := ratel.New(transaction).Run(accounts.Select().
		Where(accounts.Tenant.Eq(1).And(accounts.ID.Eq(1)))).One(t.Context())
	if err != nil || account.Email != "a@example.org" {
		t.Fatalf("%+v: %v", account, err)
	}
}

func readCancellation(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	const deadline = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	source := ratel.NewQuery([]string{"tenant", "id", "email", "manager_id"}, scanAccount,
		`SELECT 1::bigint tenant, 1::bigint id, ''::text email, NULL::bigint manager_id FROM pg_catalog.pg_sleep(30)`)

	_, err := ratel.New(pool).Run(source.Select()).List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost cancellation cause: %v", err)
	}

	if _, err := ratel.New(pool).Run(accountDescriptor().Select()).First(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func readConnectionFailure(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	connection, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var terminated bool

	var serverError *pgconn.PgError

	err = connection.QueryRow(t.Context(), "SELECT pg_catalog.pg_terminate_backend(pg_catalog.pg_backend_pid())").
		Scan(&terminated)
	if !errors.As(err, &serverError) || serverError.Code != "57P01" {
		t.Fatalf("termination: %v", err)
	}

	_, err = ratel.New(connection.Conn()).Run(accountDescriptor().Select()).First(t.Context())
	connection.Release()

	if err == nil || errors.Unwrap(err) == nil {
		t.Fatalf("missing wrapped connection failure: %v", err)
	}

	if _, err := ratel.New(pool).Run(accountDescriptor().Select()).First(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkReadScan(b *testing.B) {
	pool := readPool(b)

	for _, tc := range []struct {
		name string
		run  func(*testing.B, *pgxpool.Pool)
	}{
		{"generated", benchmarkGeneratedRead}, {"handwritten", benchmarkHandwrittenRead},
	} {
		b.Run(tc.name, func(b *testing.B) {
			tc.run(b, pool)
		})
	}
}

func BenchmarkScanner(b *testing.B) {
	pool := readPool(b)

	for _, tc := range []struct {
		name string
		run  func(*testing.B, pgx.Row)
	}{
		{"generated", benchmarkGeneratedScanner}, {"handwritten", benchmarkHandwrittenScanner},
	} {
		b.Run(tc.name, func(b *testing.B) {
			rows, err := pool.Query(b.Context(),
				"SELECT tenant, id, email, manager_id FROM read_app.accounts WHERE tenant=1 AND id=1")
			if err != nil {
				b.Fatal(err)
			}

			defer rows.Close()

			if !rows.Next() {
				b.Fatalf("missing fixture row: %v", rows.Err())
			}

			if _, err := scanAccount(rows); err != nil {
				b.Fatal(err)
			}

			tc.run(b, rows)
			rows.Close()

			if err := rows.Err(); err != nil {
				b.Fatal(err)
			}
		})
	}
}

func benchmarkGeneratedScanner(b *testing.B, row pgx.Row) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		account, err := scanAccount(row)
		if err != nil || account.ID != 1 {
			b.Fatalf("%+v: %v", account, err)
		}
	}
}

func benchmarkHandwrittenScanner(b *testing.B, row pgx.Row) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		var account readAccount

		err := row.Scan(&account.Tenant, &account.ID, &account.Email, &account.ManagerID)
		if err != nil || account.ID != 1 {
			b.Fatalf("%+v: %v", account, err)
		}
	}
}

func benchmarkGeneratedRead(b *testing.B, pool *pgxpool.Pool) {
	b.Helper()

	database := ratel.New(pool)
	accounts := accountDescriptor()

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := database.Run(accounts.Select()).List(b.Context()); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkHandwrittenRead(b *testing.B, pool *pgxpool.Pool) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := handwrittenRead(b.Context(), pool); err != nil {
			b.Fatal(err)
		}
	}
}

func handwrittenRead(ctx context.Context, pool *pgxpool.Pool) ([]readAccount, error) {
	rows, err := pool.Query(ctx, "SELECT tenant, id, email, manager_id FROM read_app.accounts")
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	accounts := make([]readAccount, 0)

	for rows.Next() {
		var account readAccount
		if err := rows.Scan(&account.Tenant, &account.ID, &account.Email, &account.ManagerID); err != nil {
			return nil, err
		}

		accounts = append(accounts, account)
	}

	return accounts, rows.Err()
}
