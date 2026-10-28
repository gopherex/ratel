package diff_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

func TestRequalify(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ text, want string }{
		{"ALTER TABLE app.old ADD CONSTRAINT k;", "ALTER TABLE app.new ADD CONSTRAINT k;"},
		{"constraint app.old.k", "constraint app.new.k"},
		{"ON app.old_log (x)", "ON app.old_log (x)"},
		{"ON xapp.old (x)", "ON xapp.old (x)"},
		{"ON other.app.old", "ON other.app.old"},
		{"app.old", "app.new"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			t.Parallel()

			if got := diff.Requalify(tc.text, "app.old", "app.new"); got != tc.want {
				t.Errorf("requalify(%q) = %q", tc.text, got)
			}
		})
	}
}

func TestRenameTables(t *testing.T) {
	t.Parallel()

	current := rendering("table app.old", "CREATE TABLE app.old (id integer);",
		"constraint app.old.k", "ALTER TABLE app.old ADD CONSTRAINT k CHECK (id > 0);")
	current.Columns = []introspect.Column{{Key: "app.old.id"}}
	desired := rendering("table app.new", "CREATE TABLE app.new (id integer);")

	for _, tc := range []struct {
		name    string
		renamed map[string]string
		want    []string
		keys    []string
	}{
		{
			name: "renamed", renamed: map[string]string{"table app.new": "old"},
			want: []string{"ALTER TABLE app.old RENAME TO new;"}, keys: []string{"table app.new", "constraint app.new.k"},
		},
		{
			name: "old name missing", renamed: map[string]string{"table app.new": "gone"},
			want: []string{}, keys: []string{"table app.old", "constraint app.old.k"},
		},
		{name: "no intent", want: []string{}, keys: []string{"table app.old", "constraint app.old.k"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			changes, renamed := diff.RenameTables(current, desired, diff.Intents{Renamed: tc.renamed})

			keys := make([]string, 0, len(renamed.Objects))
			for _, object := range renamed.Objects {
				keys = append(keys, object.Key)
			}

			if difference := cmp.Diff(tc.want, statements(changes)); difference != "" {
				t.Errorf("renameTables() SQL (-want +got):\n%s", difference)
			}

			if difference := cmp.Diff(tc.keys, keys); difference != "" {
				t.Errorf("renameTables() keys (-want +got):\n%s", difference)
			}
		})
	}
}

func TestRenamedObjects(t *testing.T) {
	t.Parallel()

	createIndex := diff.Created(introspect.Object{Key: "index app.b", SQL: "CREATE INDEX b ON app.t USING btree (x);"})
	dropIndex := diff.Dropped(introspect.Object{Key: "index app.a", SQL: "CREATE INDEX a ON app.t USING btree (x);"})
	otherIndex := diff.Dropped(introspect.Object{Key: "index app.c", SQL: "CREATE INDEX c ON app.t USING btree (y);"})
	addKey := diff.Created(introspect.Object{
		Key: "constraint app.t.b", SQL: "ALTER TABLE app.t ADD CONSTRAINT b UNIQUE (x);",
	})
	dropKey := diff.Dropped(introspect.Object{
		Key: "constraint app.t.a", SQL: "ALTER TABLE app.t ADD CONSTRAINT a UNIQUE (x);",
	})

	drops := []diff.Change{otherIndex, dropIndex, dropKey}
	got := statements(diff.RenamedObjects([]diff.Change{createIndex, addKey}, drops))
	want := []string{
		"ALTER INDEX app.a RENAME TO b;", "ALTER TABLE app.t RENAME CONSTRAINT a TO b;", "DROP INDEX app.c;",
	}

	if difference := cmp.Diff(want, got); difference != "" {
		t.Errorf("renamedObjects() (-want +got):\n%s", difference)
	}
}

func BenchmarkRequalify(b *testing.B) {
	text := "ALTER TABLE app.old ADD CONSTRAINT k FOREIGN KEY (id) REFERENCES app.old_parent (id);"

	for b.Loop() {
		diff.Requalify(text, "app.old", "app.new")
	}
}

func BenchmarkRenamedObjects(b *testing.B) {
	create := diff.Created(introspect.Object{Key: "index app.b", SQL: "CREATE INDEX b ON app.t USING btree (x);"})
	drop := diff.Dropped(introspect.Object{Key: "index app.a", SQL: "CREATE INDEX a ON app.t USING btree (x);"})

	for b.Loop() {
		diff.RenamedObjects([]diff.Change{create}, []diff.Change{drop})
	}
}
