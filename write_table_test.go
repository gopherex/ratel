package ratel_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel"
)

// compileOutput compiles source as a test file of its own package inside this
// module and returns the compiler's output, which is empty for valid code.
func compileOutput(t *testing.T, source string) string {
	t.Helper()

	file := filepath.Join(t.TempDir(), "misuse_test.go")
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := exec.CommandContext(t.Context(), "go", "test", file).CombinedOutput()
	if err == nil {
		t.Fatalf("misuse compiled: %s", source)
	}

	return string(output)
}

func TestReadOnlyDescriptorsCannotWrite(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"view.Insert()", "view.Upsert()", "view.Update()", "view.Delete()"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()

			output := compileOutput(t, `package readonly
 import "github.com/gopherex/ratel"
 type Model struct{}
 type View struct{ratel.Table[Model]}
 func invalid(view View){`+operation+`}
 `)
			if !strings.Contains(output, "undefined") {
				t.Fatalf("unexpected compiler output: %s", output)
			}
		})
	}
}

// TestVerbsFitTheBuiltValue checks that a verb or batch item that does not fit
// the kind of the built value does not compile.
func TestVerbsFitTheBuiltValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		statement string
		want      string
	}{
		{
			"list of a write without Returning", `_, _ = db.Run(tbl.Update().All()).List(ctx)`,
			"ExecRunner has no field or method List",
		},
		{
			"exec of a write with Returning", `_, _ = db.Run(tbl.Delete().All().Returning()).Exec(ctx)`,
			"RowRunner[Model] has no field or method Exec",
		},
		{
			"all of an eager select", `for range db.Run(tbl.Select().With()).All(ctx) {}`,
			"EagerRunner[Model] has no field or method All",
		},
		{
			"exec item of a select", `_ = ratel.Exec(tbl.Select(), &count)`,
			"ExecQuery",
		},
		{
			"list item of a write", `_ = ratel.List(tbl.Delete().All(), &models)`,
			"RowQuery",
		},
		{
			"insert of another table's row", `_ = tbl.Insert(Other{})`,
			"cannot use Other{} (value of struct type Other) as Ins value",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output := compileOutput(t, `package misuse
 import (
	"context"

	"github.com/gopherex/ratel"
 )
 type Model struct{}
 type Ins struct{}
 type Other struct{}
 func invalid(ctx context.Context, db *ratel.DB, tbl ratel.WriteTable[Model, Ins]) {
	var (
		count  int64
		models []Model
	)
	_, _ = count, models
	`+tc.statement+`
 }
 `)
			if !strings.Contains(output, tc.want) {
				t.Fatalf("compiler output lacks %q: %s", tc.want, output)
			}
		})
	}
}

func TestWriteMetadata(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	copied := users.InSchema(`tenant"42`)

	statement, err := build(copied.Insert(newWriteInsert("a")))
	if err != nil || !strings.Contains(statement.SQL, `"tenant""42"."users"`) {
		t.Fatalf("%+v %v", statement, err)
	}

	fields := []ratel.InsertField[writeInsert]{
		{
			Name: "email", Type: pgx.Identifier{"pg_catalog", "text"},
			Value: func(row writeInsert) (any, bool) { return row.email, true }, Values: writeEmails,
		},
	}
	table := ratel.NewWriteTable(userDescriptor().Table, func(row writeInsert) bool { return row.valid }, fields...)
	fields[0].Name = "changed"
	fields[0].Type[0] = "changed"

	statement, err = build(table.Insert(newWriteInsert("a"), newWriteInsert("b")))
	if err != nil || !strings.Contains(statement.SQL, `$1::"pg_catalog"."text"[]`) {
		t.Fatalf("%+v %v", statement, err)
	}

	for _, field := range []ratel.InsertField[writeInsert]{
		{Name: "email"},
		{Name: "email", Type: pgx.Identifier{""}, Value: func(row writeInsert) (any, bool) { return row.email, true }},
		{Name: "unknown", Type: pgx.Identifier{"text"}, Value: func(row writeInsert) (any, bool) { return row.email, true }},
	} {
		query := ratel.NewWriteTable(userDescriptor().Table,
			func(row writeInsert) bool { return row.valid }, field).Insert(newWriteInsert("a"))
		if _, err = build(query); !errors.Is(err, ratel.ErrInvalidQuery) {
			t.Fatal(err)
		}
	}
}

func BenchmarkWriteTable(b *testing.B) {
	for b.Loop() {
		users := writeDescriptor()
		_ = users.InSchema("tenant_42")
		_ = users.Email.To("a")
		_ = users.Note.To(ratel.Null[int64]{})
	}
}
