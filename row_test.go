package ratel_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gopherex/ratel"
)

type keyedUsers struct {
	ratel.KeyedTable[readUser, writeInsert]
	ID    ratel.Col[readUser, int64]
	Email ratel.Col[readUser, string]
}

// keyedDescriptor is writeDescriptor with model values and a primary key;
// valued false leaves out ModelValues.
func keyedDescriptor(valued bool) keyedUsers {
	options := []ratel.TableOption[readUser]{
		ratel.PrimaryKey([]string{"id"}, func(user readUser) []any { return []any{user.ID} }),
	}
	if valued {
		options = append(options, ratel.ModelValues(func(user readUser) []any {
			return []any{user.ID, user.Email, user.ManagerID}
		}))
	}

	table := ratel.NewTable("app", "users", []string{"id", "email", "manager_id"}, scanReadUser, options...)
	columns := writeDescriptor()

	return keyedUsers{KeyedTable: ratel.NewKeyedTable(withFields(table)), ID: columns.ID, Email: columns.Email}
}

// withFields is write metadata for email and id; manager_id is not insertable.
func withFields(table ratel.Table[readUser]) ratel.WriteTable[readUser, writeInsert] {
	return ratel.NewWriteTable(table, func(row writeInsert) bool { return row.valid },
		ratel.InsertField[writeInsert]{
			Name: "email", Type: pgx.Identifier{"pg_catalog", "text"},
			Value: func(row writeInsert) (any, bool) { return row.email, true }, Values: writeEmails,
		},
		ratel.InsertField[writeInsert]{
			Name: "id", Type: pgx.Identifier{"pg_catalog", "int8"},
			Value: func(row writeInsert) (any, bool) { return row.ID.V, row.ID.Set }, Values: writeIDs,
		})
}

func TestRowWrites(t *testing.T) {
	t.Parallel()

	users := keyedDescriptor(true)
	database := ratel.New(&pgx.Conn{})
	user := readUser{ID: 7, Email: "a@x"}

	for _, tc := range []struct {
		name  string
		write ratel.Write[readUser]
		sql   string
		args  []any
	}{
		{
			"save", database.Save(users, user),
			`UPDATE "app"."users" AS "q" SET "email" = $1 WHERE "q"."id" = $2`,
			[]any{"a@x", int64(7)},
		},
		{
			"save with version", database.Save(users, user).ExpectVersion(users.Email, "old"),
			`UPDATE "app"."users" AS "q" SET "email" = $1 WHERE ("q"."id" = $2 AND "q"."email" = $3)`,
			[]any{"a@x", int64(7), "old"},
		},
		{
			"delete row", database.DeleteRow(users, user),
			`DELETE FROM "app"."users" AS "q" WHERE "q"."id" = $1`,
			[]any{int64(7)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statement, err := tc.write.SQL()
			if err != nil || statement.SQL != tc.sql || !reflect.DeepEqual(statement.Args, tc.args) {
				t.Fatalf("got %+v, %v; want %s, %#v", statement, err, tc.sql, tc.args)
			}
		})
	}

	if _, err := database.Save(keyedDescriptor(false), user).SQL(); !errors.Is(err, ratel.ErrInvalidQuery) {
		t.Fatalf("Save without model values = %v", err)
	}
}

func TestRowWriteMissing(t *testing.T) {
	t.Parallel()

	users := keyedDescriptor(true)
	user := readUser{ID: 7, Email: "a@x"}

	save := func(db *ratel.DB) ratel.Write[readUser] { return db.Save(users, user) }

	for _, tc := range []struct {
		name  string
		write func(*ratel.DB) ratel.Write[readUser]
		rows  string
		want  error
	}{
		{"save missing", save, "UPDATE 0", ratel.ErrNotFound},
		{"stale version", func(db *ratel.DB) ratel.Write[readUser] {
			return db.Save(users, user).ExpectVersion(users.Email, "old")
		}, "UPDATE 0", ratel.ErrStaleVersion},
		{"delete missing", func(db *ratel.DB) ratel.Write[readUser] {
			return db.DeleteRow(users, user)
		}, "DELETE 0", ratel.ErrNotFound},
		{"saved", save, "UPDATE 1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executor := staticExecutor{exec: func(context.Context, string, ...any) (pgconn.CommandTag, error) {
				return pgconn.NewCommandTag(tc.rows), nil
			}}

			if _, err := tc.write(ratel.New(executor)).Exec(t.Context()); !errors.Is(err, tc.want) {
				t.Fatalf("Exec() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func BenchmarkSave(b *testing.B) {
	users := keyedDescriptor(true)
	database := ratel.New(&pgx.Conn{})
	user := readUser{ID: 7, Email: "a@x"}

	for b.Loop() {
		if _, err := database.Save(users, user).SQL(); err != nil {
			b.Fatal(err)
		}
	}
}
