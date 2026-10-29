package ratel_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gopherex/ratel"
)

func (u readUsers) InSchema(schema string) readUsers {
	u.Table = u.Table.InSchema(schema)

	return u
}

func TestTableMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		table   ratel.Table[readUser]
		suffix  string
		invalid bool
	}{
		{"schema", userDescriptor().InSchema("tenant_42").Table, `FROM "tenant_42"."users" AS "q"`, false},
		{"quoted", ratel.NewTable(`a"b`, `a.b`, []string{`a"b`}, scanReadUser), `FROM "a""b"."a.b" AS "q"`, false},
		{"zero", ratel.Table[readUser]{}, "", true},
		{"empty schema", ratel.NewTable("", "users", []string{"id"}, scanReadUser), "", true},
		{"empty name", ratel.NewTable("app", "", []string{"id"}, scanReadUser), "", true},
		{"empty column", ratel.NewTable("app", "users", []string{""}, scanReadUser), "", true},
		{"nul column", ratel.NewTable("app", "users", []string{"a\x00b"}, scanReadUser), "", true},
		{"no scanner", ratel.NewTable[readUser]("app", "users", []string{"id"}, nil), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := build(ratel.From(tc.table))
			if tc.invalid {
				if !errors.Is(err, ratel.ErrInvalidQuery) {
					t.Fatalf("error: %v", err)
				}

				return
			}

			if err != nil || !strings.HasSuffix(statement.SQL, tc.suffix) {
				t.Fatalf("%+v: %v", statement, err)
			}
		})
	}
}

func TestDescriptorCopiesInputs(t *testing.T) {
	t.Parallel()

	columns := []string{"id", "email", "manager_id"}
	args := []any{int64(1)}
	table := ratel.NewTable("app", "users", columns, scanReadUser)
	query := ratel.NewQuery(columns, scanReadUser, "SELECT $1 AS id, '' AS email, NULL AS manager_id", args...)
	columns[0] = "changed"
	args[0] = int64(2)

	tableSQL, err := build(ratel.From(table))
	if err != nil || !strings.HasPrefix(tableSQL.SQL, `SELECT "q"."id",`) {
		t.Fatalf("%+v: %v", tableSQL, err)
	}

	querySQL, err := build(ratel.From(ratel.From(query)))
	if err != nil || querySQL.Args[0] != int64(1) {
		t.Fatalf("%+v: %v", querySQL, err)
	}
}

func TestBwStaticSourceInvalid(t *testing.T) {
	t.Parallel()

	columns := []string{"id", "email", "manager_id"}

	for name, query := range map[string]ratel.Select[readUser]{
		"nested empty SQL": ratel.From(ratel.From(ratel.NewQuery(columns, scanReadUser, ""))),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := build(query); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("SQL() error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func TestStaticBuild(t *testing.T) {
	t.Parallel()

	columns := []string{"id", "email", "manager_id"}
	args := []any{int64(1)}
	query := ratel.NewQuery(columns, scanReadUser, "SELECT $1", args...)
	command := ratel.NewCommand("DELETE FROM app.users WHERE id = $1", args...)

	args[0] = int64(2)

	for name, value := range map[string]buildable{
		"query": query, "command": command, "query select": query.Select(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sql, got, err := value.Build()
			if err != nil || sql == "" || got[0] != int64(1) {
				t.Fatalf("Build() = %q, %v, %v", sql, got, err)
			}

			got[0] = int64(3)

			if again, _ := build(value); again.Args[0] != int64(1) {
				t.Fatal("Build() shares the statement's arguments")
			}
		})
	}

	for name, value := range map[string]buildable{
		"empty query": ratel.NewQuery(columns, scanReadUser, ""), "empty command": ratel.NewCommand(""),
		"zero query": ratel.Query[readUser]{}, "zero command": ratel.Command{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, _, err := value.Build(); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("Build() error = %v", err)
			}
		})
	}
}

func BenchmarkStaticBuild(b *testing.B) {
	query := ratel.NewQuery([]string{"id", "email", "manager_id"}, scanReadUser, "SELECT $1", int64(1))

	b.ReportAllocs()

	for b.Loop() {
		if _, _, err := query.Build(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStaticSource(b *testing.B) {
	users := userDescriptor()

	b.ReportAllocs()

	for b.Loop() {
		source := ratel.NewQuery([]string{"id", "email", "manager_id"}, scanReadUser,
			"SELECT id, email, manager_id FROM app.users WHERE id > $1", int64(1))

		_, err := build(ratel.From(ratel.From(source)).Where(users.ID.Lt(10)).
			OrderBy(users.ID.Asc()).Limit(2).Offset(1))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDescriptors(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		_, err := build(ratel.From(userDescriptor().InSchema("tenant_42")))
		if err != nil {
			b.Fatal(err)
		}
	}
}
