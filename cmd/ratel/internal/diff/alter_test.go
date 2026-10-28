package diff_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// viewColumns is a rendering with view app.v of the column types.
func viewColumns(types ...string) introspect.Rendering {
	var result introspect.Rendering
	for index, name := range types {
		result.Columns = append(result.Columns, introspect.Column{Key: "app.v." + string(rune('a'+index)), Type: name})
	}

	return result
}

func TestAltered(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		key              string
		before, after    string
		current, desired introspect.Rendering
		want             []string
	}{
		{
			name: "sequence", key: "sequence app.s",
			after: "CREATE SEQUENCE app.s AS integer INCREMENT BY 2 MINVALUE 1 MAXVALUE 9 START WITH 1 CACHE 1;",
			want: []string{
				"ALTER SEQUENCE app.s AS integer INCREMENT BY 2 MINVALUE 1 MAXVALUE 9 START WITH 1 CACHE 1 NO CYCLE " +
					"OWNED BY NONE;",
			},
		},
		{
			name: "routine body", key: "routine app.f()",
			before: "CREATE OR REPLACE FUNCTION app.f()\n RETURNS integer\n AS $$SELECT 1$$;",
			after:  "CREATE OR REPLACE FUNCTION app.f()\n RETURNS integer\n AS $$SELECT 2$$;",
			want:   []string{"CREATE OR REPLACE FUNCTION app.f()\n RETURNS integer\n AS $$SELECT 2$$;"},
		},
		{
			name: "routine result", key: "routine app.f()",
			before: "CREATE OR REPLACE FUNCTION app.f()\n RETURNS integer\n AS $$SELECT 1$$;",
			after:  "CREATE OR REPLACE FUNCTION app.f()\n RETURNS bigint\n AS $$SELECT 1$$;",
			want: []string{
				"DROP FUNCTION app.f();", "CREATE OR REPLACE FUNCTION app.f()\n RETURNS bigint\n AS $$SELECT 1$$;",
			},
		},
		{
			name: "view appends", key: "view app.v", current: viewColumns("integer"),
			desired: viewColumns("integer", "text"),
			before:  "CREATE VIEW app.v AS SELECT 1;", after: "CREATE VIEW app.v AS SELECT 1, 'x';",
			want: []string{"CREATE OR REPLACE VIEW app.v AS SELECT 1, 'x';"},
		},
		{
			name: "view retypes", key: "view app.v", current: viewColumns("integer"), desired: viewColumns("bigint"),
			before: "CREATE VIEW app.v AS SELECT 1;", after: "CREATE VIEW app.v AS SELECT 1::bigint;",
			want: []string{"DROP VIEW app.v;", "CREATE VIEW app.v AS SELECT 1::bigint;"},
		},
		{
			name: "view loses a column", key: "view app.v", current: viewColumns("integer", "text"),
			desired: viewColumns("integer"),
			before:  "CREATE VIEW app.v AS SELECT 1, 'x';", after: "CREATE VIEW app.v AS SELECT 1;",
			want: []string{"DROP VIEW app.v;", "CREATE VIEW app.v AS SELECT 1;"},
		},
		{
			name: "rls", key: "rls app.t", after: "ALTER TABLE app.t FORCE ROW LEVEL SECURITY;",
			want: []string{"ALTER TABLE app.t DISABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;"},
		},
		{
			name: "comment", key: "comment table app.t", after: "COMMENT ON TABLE app.t IS 'x';",
			want: []string{"COMMENT ON TABLE app.t IS 'x';"},
		},
		{
			name: "extension", key: "extension citext", after: "CREATE EXTENSION citext WITH SCHEMA app VERSION '1.6';",
			want: []string{"ALTER EXTENSION citext SET SCHEMA app;\nALTER EXTENSION citext UPDATE TO '1.6';"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := diff.Altered(tc.current, tc.desired, diff.Intents{}, tc.key, tc.before, tc.after)
			if difference := cmp.Diff(tc.want, statements(result.Changes)); difference != "" || len(result.Drops) != 0 {
				t.Errorf("altered() (-want +got):\n%s\ndrops: %v", difference, result.Drops)
			}
		})
	}
}

func TestAlteredRLS(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		after string
		kind  diff.Kind
		sql   string
	}{
		{
			"ALTER TABLE app.t ENABLE ROW LEVEL SECURITY;", diff.EnableRLS,
			"ALTER TABLE app.t ENABLE ROW LEVEL SECURITY, NO FORCE ROW LEVEL SECURITY;",
		},
		{
			"ALTER TABLE app.t ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;", diff.EnableRLS,
			"ALTER TABLE app.t ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;",
		},
	} {
		t.Run(tc.after, func(t *testing.T) {
			t.Parallel()

			if change := diff.AlteredRLS("rls app.t", "app.t", tc.after); change.Kind != tc.kind || change.SQL != tc.sql {
				t.Errorf("alteredRLS() = %+v", change)
			}
		})
	}
}

func TestAlterSequence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ create, want string }{
		{
			"CREATE SEQUENCE app.s AS bigint CYCLE OWNED BY app.t.id;",
			"ALTER SEQUENCE app.s AS bigint CYCLE OWNED BY app.t.id;",
		},
		{"CREATE SEQUENCE app.s AS bigint;", "ALTER SEQUENCE app.s AS bigint NO CYCLE OWNED BY NONE;"},
	} {
		t.Run(tc.create, func(t *testing.T) {
			t.Parallel()

			if got := diff.SequenceAlteration(tc.create); got != tc.want {
				t.Errorf("alterSequence() = %q", got)
			}
		})
	}
}

func TestSignature(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ definition, want string }{
		{
			"CREATE OR REPLACE FUNCTION app.f(a integer)\n RETURNS integer\n LANGUAGE sql\nAS $$x$$;",
			"CREATE OR REPLACE FUNCTION app.f(a integer)\ninteger",
		},
		{"CREATE OR REPLACE PROCEDURE app.p()\n LANGUAGE sql\nAS $$x$$;", "CREATE OR REPLACE PROCEDURE app.p()\n"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			if got := diff.Signature(tc.definition); got != tc.want {
				t.Errorf("signature() = %q", got)
			}
		})
	}
}

func TestAppendsColumns(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		current, desired introspect.Rendering
		want             bool
	}{
		{"same", viewColumns("integer"), viewColumns("integer"), true},
		{"appended", viewColumns("integer"), viewColumns("integer", "text"), true},
		{"retyped", viewColumns("integer"), viewColumns("text"), false},
		{"removed", viewColumns("integer", "text"), viewColumns("integer"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := diff.AppendsColumns(tc.current, tc.desired, "app.v"); got != tc.want {
				t.Errorf("appendsColumns() = %v", got)
			}
		})
	}
}

func BenchmarkAltered(b *testing.B) {
	before := "CREATE OR REPLACE FUNCTION app.f()\n RETURNS integer\n AS $$SELECT 1$$;"
	after := "CREATE OR REPLACE FUNCTION app.f()\n RETURNS integer\n AS $$SELECT 2$$;"

	for b.Loop() {
		diff.Altered(introspect.Rendering{}, introspect.Rendering{}, diff.Intents{}, "routine app.f()", before, after)
	}
}

func BenchmarkAppendsColumns(b *testing.B) {
	current, desired := viewColumns("integer"), viewColumns("integer", "text")

	for b.Loop() {
		diff.AppendsColumns(current, desired, "app.v")
	}
}
