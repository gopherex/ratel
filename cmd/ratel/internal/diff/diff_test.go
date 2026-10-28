package diff_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

func rendering(objects ...string) introspect.Rendering {
	var result introspect.Rendering

	for index := 0; index < len(objects); index += 2 {
		result.Objects = append(result.Objects, introspect.Object{Key: objects[index], SQL: objects[index+1]})
	}

	return result
}

// statements is the SQL of each change.
func statements(changes []diff.Change) []string {
	result := make([]string, 0, len(changes))
	for index := range changes {
		result = append(result, changes[index].SQL)
	}

	return result
}

func TestDiff(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		current, desired introspect.Rendering
		want             []string
	}{
		{
			name: "equal", current: rendering("schema app", "CREATE SCHEMA app;"),
			desired: rendering("schema app", "CREATE SCHEMA app;"), want: []string{},
		},
		{
			name:    "create in order",
			desired: rendering("schema app", "CREATE SCHEMA app;", "sequence app.s", "CREATE SEQUENCE app.s;"),
			want:    []string{"CREATE SCHEMA app;", "CREATE SEQUENCE app.s;"},
		},
		{
			name:    "drop in reverse",
			current: rendering("schema app", "CREATE SCHEMA app;", "sequence app.s", "CREATE SEQUENCE app.s;"),
			want:    []string{"DROP SEQUENCE app.s;", "DROP SCHEMA app;"},
		},
		{
			name:    "replace",
			current: rendering("trigger app.t.x", "CREATE TRIGGER x AFTER INSERT ON app.t EXECUTE FUNCTION app.f();"),
			desired: rendering("trigger app.t.x", "CREATE TRIGGER x AFTER UPDATE ON app.t EXECUTE FUNCTION app.f();"),
			want: []string{
				"DROP TRIGGER x ON app.t;",
				"CREATE TRIGGER x AFTER UPDATE ON app.t EXECUTE FUNCTION app.f();",
			},
		},
		{
			name:    "rename index",
			current: rendering("index app.a", "CREATE INDEX a ON app.t USING btree (x);"),
			desired: rendering("index app.b", "CREATE INDEX b ON app.t USING btree (x);"),
			want:    []string{"ALTER INDEX app.a RENAME TO b;"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := statements(diff.Diff(tc.current, tc.desired, diff.Intents{}))
			if difference := cmp.Diff(tc.want, got); difference != "" {
				t.Errorf("Diff() SQL (-want +got):\n%s", difference)
			}
		})
	}
}

func TestColumnsDiffer(t *testing.T) {
	t.Parallel()

	current := introspect.Rendering{Columns: []introspect.Column{{Key: "app.p1.k", NotNull: " NOT NULL"}}}
	desired := introspect.Rendering{Columns: []introspect.Column{{Key: "app.p1.k"}}}

	for _, tc := range []struct {
		key  string
		want bool
	}{
		{key: "table app.p1", want: true},
		{key: "table app.p", want: false},
		{key: "view app.p1", want: false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()

			if got := diff.ColumnsDiffer(current, desired, tc.key); got != tc.want {
				t.Errorf("columnsDiffer(%q) = %v", tc.key, got)
			}
		})
	}
}

func TestKindOfParentOf(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ key, kind, name, parent string }{
		{key: "table app.t", kind: "table", name: "app.t", parent: "app"},
		{key: "constraint app.t.c", kind: "constraint", name: "app.t.c", parent: "app.t"},
		{key: "schema app", kind: "schema", name: "app", parent: "app"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()

			kind, name := diff.KindOf(tc.key), diff.NameOf(tc.key)
			if kind != tc.kind || name != tc.name || diff.ParentOf(name) != tc.parent {
				t.Errorf("kindOf(%q) = %q, %q; parentOf = %q", tc.key, kind, name, diff.ParentOf(name))
			}
		})
	}
}

func TestCreated(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		key, sql string
		kind     diff.Kind
		table    string
	}{
		{"schema app", "CREATE SCHEMA app;", diff.CreateSchema, ""},
		{"extension x", "CREATE EXTENSION x;", diff.CreateExtension, ""},
		{"type app.e", "CREATE TYPE app.e AS ENUM ();", diff.CreateType, ""},
		{"sequence app.s", "CREATE SEQUENCE app.s;", diff.CreateSequence, ""},
		{"routine app.f()", "CREATE FUNCTION app.f()", diff.CreateRoutine, ""},
		{"table app.t", "CREATE TABLE app.t ();", diff.CreateTable, "app.t"},
		{"table app.c", "CREATE TABLE app.c PARTITION OF app.t DEFAULT;", diff.AttachPartition, "app.c"},
		{"constraint app.t.k", "ALTER TABLE app.t ADD CONSTRAINT k PRIMARY KEY (id);", diff.AddPrimaryKey, "app.t"},
		{"constraint app.t.u", "ALTER TABLE app.t ADD CONSTRAINT u UNIQUE (id);", diff.AddUnique, "app.t"},
		{
			"constraint app.t.f", "ALTER TABLE app.t ADD CONSTRAINT f FOREIGN KEY (id) REFERENCES app.u(id);",
			diff.AddForeignKey, "app.t",
		},
		{
			"constraint app.t.x", "ALTER TABLE app.t ADD CONSTRAINT x EXCLUDE USING gist (r WITH &&);",
			diff.AddExclusion, "app.t",
		},
		{"constraint app.t.c", "ALTER TABLE app.t ADD CONSTRAINT c CHECK (id > 0);", diff.AddCheck, "app.t"},
		{"index app.i", "CREATE INDEX i ON ONLY app.t USING btree (x);", diff.CreateIndex, "app.t"},
		{"view app.v", "CREATE VIEW app.v AS SELECT 1;", diff.CreateView, ""},
		{"trigger app.t.x", "CREATE TRIGGER x", diff.CreateTrigger, "app.t"},
		{"policy app.t.p", "CREATE POLICY p ON app.t;", diff.CreatePolicy, "app.t"},
		{"rls app.t", "ALTER TABLE app.t ENABLE ROW LEVEL SECURITY;", diff.EnableRLS, "app.t"},
		{"grant table app.t r false", "GRANT SELECT ON app.t TO r;", diff.Grant, ""},
		{"comment table app.t", "COMMENT ON TABLE app.t IS 'x';", diff.Comment, ""},
	} {
		t.Run(tc.key+" "+string(tc.kind), func(t *testing.T) {
			t.Parallel()

			change := diff.Created(introspect.Object{Key: tc.key, SQL: tc.sql})
			if change.Kind != tc.kind || change.Table != tc.table || change.SQL != tc.sql {
				t.Errorf("created() = %+v", change)
			}
		})
	}
}

func BenchmarkDiff(b *testing.B) {
	current := rendering("schema app", "CREATE SCHEMA app;", "index app.a", "CREATE INDEX a ON app.t USING btree (x);",
		"sequence app.s", "CREATE SEQUENCE app.s;")
	desired := rendering("schema app", "CREATE SCHEMA app;", "index app.b", "CREATE INDEX b ON app.t USING btree (x);",
		"sequence app.s", "CREATE SEQUENCE app.s CYCLE;")

	for b.Loop() {
		diff.Diff(current, desired, diff.Intents{})
	}
}
