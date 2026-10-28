package ratel_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

func (u readUsers) InSchema(schema string) readUsers {
	u.Table = u.Table.InSchema(schema)

	return u
}

func TestTableMetadata(t *testing.T) {
	t.Parallel()

	database := ratel.New(&pgx.Conn{})

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

			statement, err := database.From(tc.table).SQL()
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
	database := ratel.New(&pgx.Conn{})

	tableSQL, err := database.From(table).SQL()
	if err != nil || !strings.HasPrefix(tableSQL.SQL, `SELECT "q"."id",`) {
		t.Fatalf("%+v: %v", tableSQL, err)
	}

	querySQL, err := database.From(database.From(query)).SQL()
	if err != nil || querySQL.Args[0] != int64(1) {
		t.Fatalf("%+v: %v", querySQL, err)
	}
}

func TestBwStaticSourceInvalid(t *testing.T) {
	t.Parallel()

	database := ratel.New(&pgx.Conn{})
	columns := []string{"id", "email", "manager_id"}

	for name, query := range map[string]ratel.Select[readUser]{
		"nested empty SQL": database.From(database.From(ratel.NewQuery(columns, scanReadUser, ""))),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := query.SQL(); !errors.Is(err, ratel.ErrInvalidQuery) {
				t.Fatalf("SQL() error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func BenchmarkStaticSource(b *testing.B) {
	database := ratel.New(&pgx.Conn{})
	users := userDescriptor()

	b.ReportAllocs()

	for b.Loop() {
		source := ratel.NewQuery([]string{"id", "email", "manager_id"}, scanReadUser,
			"SELECT id, email, manager_id FROM app.users WHERE id > $1", int64(1))

		_, err := database.From(database.From(source)).Where(users.ID.Lt(10)).
			OrderBy(users.ID.Asc()).Limit(2).Offset(1).SQL()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDescriptors(b *testing.B) {
	database := ratel.New(&pgx.Conn{})

	b.ReportAllocs()

	for b.Loop() {
		_, err := database.From(userDescriptor().InSchema("tenant_42")).SQL()
		if err != nil {
			b.Fatal(err)
		}
	}
}
