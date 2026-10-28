package diff_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

func TestAlteredColumn(t *testing.T) {
	t.Parallel()

	stored := " GENERATED ALWAYS AS ((id * 2)) STORED"

	for _, tc := range []struct {
		name          string
		major         int
		before, after introspect.Column
		retype        string
		want          []string
	}{
		{
			name: "type with using", before: introspect.Column{Type: "text"}, after: introspect.Column{Type: "integer"},
			retype: "c::integer", want: []string{"ALTER TABLE app.t ALTER COLUMN c TYPE integer USING c::integer;"},
		},
		{
			name: "serial type", before: introspect.Column{Type: "serial"}, after: introspect.Column{Type: "bigserial"},
			want: []string{"ALTER TABLE app.t ALTER COLUMN c TYPE bigint;"},
		},
		{
			name:   "default collation",
			before: introspect.Column{Type: "text", Collation: ` COLLATE pg_catalog."C"`},
			after:  introspect.Column{Type: "text"},
			want:   []string{`ALTER TABLE app.t ALTER COLUMN c TYPE text COLLATE pg_catalog."default";`},
		},
		{
			name: "defaults", before: introspect.Column{Type: "integer", Default: "1"},
			after: introspect.Column{Type: "bigint"},
			want: []string{
				"ALTER TABLE app.t ALTER COLUMN c DROP DEFAULT;", "ALTER TABLE app.t ALTER COLUMN c TYPE bigint;",
			},
		},
		{
			name: "identity dropped",
			before: introspect.Column{
				Identity: " GENERATED ALWAYS AS IDENTITY (START WITH 1)", NotNull: " NOT NULL",
			},
			want: []string{
				"ALTER TABLE app.t ALTER COLUMN c DROP IDENTITY;", "ALTER TABLE app.t ALTER COLUMN c DROP NOT NULL;",
			},
		},
		{
			name: "named not null", major: 18, after: introspect.Column{NotNull: " CONSTRAINT t_c_not_null NOT NULL"},
			want: []string{"ALTER TABLE app.t ADD CONSTRAINT t_c_not_null NOT NULL c;"},
		},
		{
			name: "expression set", major: 17, before: introspect.Column{Generated: stored},
			after: introspect.Column{Generated: " GENERATED ALWAYS AS ((id * 3)) STORED"},
			want:  []string{"ALTER TABLE app.t ALTER COLUMN c SET EXPRESSION AS ((id * 3));"},
		},
		{
			name: "expression before 17", major: 16, before: introspect.Column{Generated: stored},
			after: introspect.Column{
				Definition: "c integer GENERATED ALWAYS AS ((id * 3)) STORED",
				Generated:  " GENERATED ALWAYS AS ((id * 3)) STORED",
			},
			want: []string{
				"ALTER TABLE app.t DROP COLUMN c;\n" +
					"ALTER TABLE app.t ADD COLUMN c integer GENERATED ALWAYS AS ((id * 3)) STORED;",
			},
		},
		{
			name: "expression dropped", major: 18, before: introspect.Column{Generated: stored},
			want: []string{"ALTER TABLE app.t ALTER COLUMN c DROP EXPRESSION;"},
		},
		{
			name: "virtual dropped", major: 18, before: introspect.Column{Generated: " GENERATED ALWAYS AS (1) VIRTUAL"},
			after: introspect.Column{Definition: "c integer"},
			want:  []string{"ALTER TABLE app.t DROP COLUMN c;\nALTER TABLE app.t ADD COLUMN c integer;"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			intents := diff.Intents{Retype: map[string]string{"column app.t.c": tc.retype}}
			got := statements(diff.AlteredColumn(tc.major, tc.before, tc.after, intents))

			if difference := cmp.Diff(tc.want, got); difference != "" {
				t.Errorf("alteredColumn() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestAlteredColumns(t *testing.T) {
	t.Parallel()

	current := introspect.Rendering{Columns: []introspect.Column{{Key: "app.t.old"}, {Key: "app.t.gone"}}}
	desired := introspect.Rendering{Columns: []introspect.Column{
		{Key: "app.t.fresh"}, {Key: "app.t.added", Definition: "added text"},
	}}
	intents := diff.Intents{Renamed: map[string]string{"column app.t.fresh": "old"}}
	result := diff.AlteredColumns(current, desired, intents, "app.t")

	want := []string{
		"ALTER TABLE app.t RENAME COLUMN old TO fresh;", "ALTER TABLE app.t ADD COLUMN added text;",
		"ALTER TABLE app.t DROP COLUMN gone;",
	}
	if difference := cmp.Diff(want, statements(append(result.Changes, result.Drops...))); difference != "" {
		t.Errorf("alteredColumns() (-want +got):\n%s", difference)
	}
}

func TestAlteredTable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		before, after string
		want          []string
	}{
		{
			name: "partition key", before: "CREATE TABLE app.t (k integer) PARTITION BY RANGE (k);",
			after: "CREATE TABLE app.t (k integer) PARTITION BY LIST (k);",
			want:  []string{"DROP TABLE app.t;", "CREATE TABLE app.t (k integer) PARTITION BY LIST (k);"},
		},
		{
			name: "logged", before: "CREATE UNLOGGED TABLE app.t ();", after: "CREATE TABLE app.t ();",
			want: []string{"ALTER TABLE app.t SET LOGGED;"},
		},
		{
			name: "detached", before: "CREATE TABLE app.t PARTITION OF app.p DEFAULT;", after: "CREATE TABLE app.t ();",
			want: []string{"ALTER TABLE app.p DETACH PARTITION app.t;"},
		},
		{
			name: "attached", before: "CREATE TABLE app.t ();", after: "CREATE TABLE app.t PARTITION OF app.p DEFAULT;",
			want: []string{"ALTER TABLE app.p ATTACH PARTITION app.t DEFAULT;"},
		},
		{
			name:   "moved",
			before: "CREATE TABLE app.t PARTITION OF app.p DEFAULT;",
			after:  "CREATE TABLE app.t PARTITION OF app.q FOR VALUES IN (1) PARTITION BY LIST (k);",
			want: []string{
				"ALTER TABLE app.p DETACH PARTITION app.t;\nDROP TABLE app.t;",
				"CREATE TABLE app.t PARTITION OF app.q FOR VALUES IN (1) PARTITION BY LIST (k);",
			},
		},
		{
			name: "bound", before: "CREATE TABLE app.t PARTITION OF app.p FOR VALUES IN (1);",
			after: "CREATE TABLE app.t PARTITION OF app.p FOR VALUES IN (2);",
			want: []string{
				"ALTER TABLE app.p DETACH PARTITION app.t;", "ALTER TABLE app.p ATTACH PARTITION app.t FOR VALUES IN (2);",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := diff.AlteredTable(introspect.Rendering{}, introspect.Rendering{}, diff.Intents{}, "app.t",
				tc.before, tc.after)
			if difference := cmp.Diff(tc.want, statements(result.Changes)); difference != "" {
				t.Errorf("alteredTable() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestPartitionColumns(t *testing.T) {
	t.Parallel()

	current := introspect.Rendering{Columns: []introspect.Column{{Key: "app.t.k", NotNull: " CONSTRAINT a NOT NULL"}}}
	desired := introspect.Rendering{Columns: []introspect.Column{{Key: "app.t.k", NotNull: " CONSTRAINT b NOT NULL"}}}

	got := statements(diff.PartitionColumns(current, desired, "app.t"))
	if difference := cmp.Diff([]string{"ALTER TABLE app.t RENAME CONSTRAINT a TO b;"}, got); difference != "" {
		t.Errorf("partitionColumns() (-want +got):\n%s", difference)
	}
}

func TestIdentityOptions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ clause, want string }{
		{
			" GENERATED ALWAYS AS IDENTITY (START WITH 1 INCREMENT BY 2 MINVALUE 1 MAXVALUE 9 CACHE 1)",
			"SET GENERATED ALWAYS SET START WITH 1 SET INCREMENT BY 2 SET MINVALUE 1 SET MAXVALUE 9 SET CACHE 1 " +
				"SET NO CYCLE",
		},
		{
			" GENERATED BY DEFAULT AS IDENTITY (START WITH 1 INCREMENT BY 1 MINVALUE 1 MAXVALUE 9 CACHE 1 CYCLE)",
			"SET GENERATED BY DEFAULT SET START WITH 1 SET INCREMENT BY 1 SET MINVALUE 1 SET MAXVALUE 9 SET CACHE 1 " +
				"SET CYCLE",
		},
	} {
		t.Run(tc.clause, func(t *testing.T) {
			t.Parallel()

			if got := diff.IdentityOptions(tc.clause); got != tc.want {
				t.Errorf("identityOptions() = %q", got)
			}
		})
	}
}

func TestPlainType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, want string }{
		{"smallserial", "smallint"}, {"serial", "integer"}, {"bigserial", "bigint"}, {"text", "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := diff.PlainType(tc.name); got != tc.want {
				t.Errorf("plainType(%q) = %q", tc.name, got)
			}
		})
	}
}

func BenchmarkAlteredColumn(b *testing.B) {
	before := introspect.Column{Type: "integer", Default: "1", NotNull: " NOT NULL"}
	after := introspect.Column{Type: "bigint", Default: "2", Identity: " GENERATED ALWAYS AS IDENTITY (START WITH 1)"}

	for b.Loop() {
		diff.AlteredColumn(18, before, after, diff.Intents{})
	}
}

func BenchmarkAlteredColumns(b *testing.B) {
	current := introspect.Rendering{Columns: []introspect.Column{{Key: "app.t.a"}, {Key: "app.t.b"}}}
	desired := introspect.Rendering{Columns: []introspect.Column{{Key: "app.t.a", Type: "text"}, {Key: "app.t.c"}}}

	for b.Loop() {
		diff.AlteredColumns(current, desired, diff.Intents{}, "app.t")
	}
}
