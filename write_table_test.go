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

func TestReadOnlyDescriptorsCannotWrite(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"db.Insert(view)", "db.Upsert(view)", "db.Update(view)", "db.Delete(view)"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			file := filepath.Join(t.TempDir(), "readonly_test.go")
			source := `package readonly
 import "github.com/gopherex/ratel"
 type Model struct{}
 type View struct{ratel.Table[Model]}
 func invalid(db *ratel.DB,view View){` + operation + `}
 `

			if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}

			output, err := exec.CommandContext(t.Context(), "go", "test", file).CombinedOutput()
			if err == nil || !strings.Contains(string(output), "Writable") {
				t.Fatalf("read-only descriptor compiled: %s %v", output, err)
			}
		})
	}
}

func TestWriteMetadata(t *testing.T) {
	t.Parallel()

	users := writeDescriptor()
	database := ratel.New(readExecutor{})
	copied := users.InSchema(`tenant"42`)

	statement, err := database.Insert(copied, newWriteInsert("a")).SQL()
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

	statement, err = database.Insert(table, newWriteInsert("a"), newWriteInsert("b")).SQL()
	if err != nil || !strings.Contains(statement.SQL, `$1::"pg_catalog"."text"[]`) {
		t.Fatalf("%+v %v", statement, err)
	}

	for _, field := range []ratel.InsertField[writeInsert]{
		{Name: "email"},
		{Name: "email", Type: pgx.Identifier{""}, Value: func(row writeInsert) (any, bool) { return row.email, true }},
		{Name: "unknown", Type: pgx.Identifier{"text"}, Value: func(row writeInsert) (any, bool) { return row.email, true }},
	} {
		query := database.Insert(ratel.NewWriteTable(userDescriptor().Table,
			func(row writeInsert) bool { return row.valid }, field), newWriteInsert("a"))
		if _, err = query.SQL(); !errors.Is(err, ratel.ErrInvalidQuery) {
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
