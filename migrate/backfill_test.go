package migrate_test

import (
	"errors"
	"testing"

	"github.com/gopherex/ratel/migrate"
)

func TestSplitUpdate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		statement string
		want      [3]string
	}{
		{"UPDATE app.t SET a = 1", [3]string{"app.t", "UPDATE app.t SET a = 1", "true"}},
		{
			"UPDATE ONLY app.t SET a = (SELECT 1 WHERE true) WHERE b IS NULL",
			[3]string{"app.t", "UPDATE ONLY app.t SET a = (SELECT 1 WHERE true)", "b IS NULL"},
		},
		{"UPDATE app.t SET a = ' WHERE ' WHERE b", [3]string{"app.t", "UPDATE app.t SET a = ' WHERE '", "b"}},
		{"UPDATE app.t SET a = 1\nWHERE\tb", [3]string{"app.t", "UPDATE app.t SET a = 1", "b"}},
		{"UPDATE app.t SET a = 1 where b", [3]string{"app.t", "UPDATE app.t SET a = 1", "b"}},
		{
			`UPDATE app.t SET "WHERE" = 1, nowhere = 2`,
			[3]string{"app.t", `UPDATE app.t SET "WHERE" = 1, nowhere = 2`, "true"},
		},
		{"UPDATE app.t SET a = E'\\' WHERE ' WHERE b", [3]string{"app.t", "UPDATE app.t SET a = E'\\' WHERE '", "b"}},
		{
			"UPDATE app.t SET a = 1 /* /* */ WHERE */ WHERE b",
			[3]string{"app.t", "UPDATE app.t SET a = 1 /* /* */ WHERE */", "b"},
		},
	} {
		t.Run(tc.statement, func(t *testing.T) {
			t.Parallel()

			if got, err := migrate.SplitUpdate(tc.statement); err != nil || got != tc.want {
				t.Errorf("splitUpdate() = %q, %v", got, err)
			}
		})
	}

	if _, err := migrate.SplitUpdate("DELETE FROM app.t"); !errors.Is(err, migrate.ErrBackfill) {
		t.Errorf("DELETE = %v", err)
	}
}

func BenchmarkTopLevelWhere(b *testing.B) {
	statement := "UPDATE app.t SET a = (SELECT 1 FROM x WHERE y) WHERE b IS NULL AND c = ' WHERE '"

	for b.Loop() {
		migrate.TopLevelWhere(statement)
	}
}
