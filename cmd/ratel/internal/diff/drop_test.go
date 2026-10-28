package diff_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

func TestDropped(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		key, sql string
		kind     diff.Kind
		want     string
	}{
		{"schema app", "", diff.DropSchema, "DROP SCHEMA app;"},
		{"extension citext", "", diff.DropExtension, "DROP EXTENSION citext;"},
		{"type app.d", "CREATE DOMAIN app.d AS integer;", diff.DropType, "DROP DOMAIN app.d;"},
		{"type app.e", "CREATE TYPE app.e AS ENUM ();", diff.DropType, "DROP TYPE app.e;"},
		{"sequence app.s", "", diff.DropSequence, "DROP SEQUENCE app.s;"},
		{
			"routine app.p(integer)", "CREATE OR REPLACE PROCEDURE app.p(a integer)",
			diff.DropRoutine, "DROP PROCEDURE app.p(integer);",
		},
		{"routine app.f()", "CREATE OR REPLACE FUNCTION app.f()", diff.DropRoutine, "DROP FUNCTION app.f();"},
		{"table app.t", "CREATE TABLE app.t ();", diff.DropTable, "DROP TABLE app.t;"},
		{
			"table app.c", "CREATE TABLE app.c PARTITION OF app.t DEFAULT;",
			diff.DetachPartition, "ALTER TABLE app.t DETACH PARTITION app.c;\nDROP TABLE app.c;",
		},
		{"constraint app.t.k", "", diff.DropConstraint, "ALTER TABLE app.t DROP CONSTRAINT k;"},
		{"index app.i", "CREATE INDEX i ON app.t USING btree (x);", diff.DropIndex, "DROP INDEX app.i;"},
		{
			"view app.m", "CREATE MATERIALIZED VIEW app.m AS SELECT 1 WITH DATA;",
			diff.DropView, "DROP MATERIALIZED VIEW app.m;",
		},
		{"view app.v", "CREATE VIEW app.v AS SELECT 1;", diff.DropView, "DROP VIEW app.v;"},
		{"trigger app.t.x", "", diff.DropTrigger, "DROP TRIGGER x ON app.t;"},
		{"policy app.t.p", "", diff.DropPolicy, "DROP POLICY p ON app.t;"},
		{
			"rls app.t", "", diff.DisableRLS,
			"ALTER TABLE app.t DISABLE ROW LEVEL SECURITY, NO FORCE ROW LEVEL SECURITY;",
		},
		{
			"grant table app.t r true", "GRANT SELECT, INSERT ON app.t TO r WITH GRANT OPTION;",
			diff.Revoke, "REVOKE SELECT, INSERT ON app.t FROM r;",
		},
		{"comment table column app.t.c", "", diff.Comment, "COMMENT ON COLUMN app.t.c IS NULL;"},
		{"comment table constraint k on app.t", "", diff.Comment, "COMMENT ON CONSTRAINT k on app.t IS NULL;"},
		{"comment materialized view app.m", "", diff.Comment, "COMMENT ON MATERIALIZED VIEW app.m IS NULL;"},
		{"comment function app.f()", "", diff.Comment, "COMMENT ON FUNCTION app.f() IS NULL;"},
	} {
		t.Run(tc.key+" "+string(tc.kind), func(t *testing.T) {
			t.Parallel()

			change := diff.Dropped(introspect.Object{Key: tc.key, SQL: tc.sql})
			if change.Kind != tc.kind || change.SQL != tc.want || change.Before != tc.sql {
				t.Errorf("dropped() = %+v, want %s %q", change, tc.kind, tc.want)
			}
		})
	}
}

func BenchmarkDropped(b *testing.B) {
	object := introspect.Object{Key: "grant table app.t r true", SQL: "GRANT SELECT ON app.t TO r WITH GRANT OPTION;"}

	for b.Loop() {
		diff.Dropped(object)
	}
}
