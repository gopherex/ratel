package plan_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

func TestRetype(t *testing.T) {
	t.Parallel()

	retype := diff.Change{
		Kind: diff.AlterColumnType, Key: "column app.t.v", Table: "app.t", Column: "v",
		SQL: "ALTER TABLE app.t ALTER COLUMN v TYPE bigint;",
	}
	target := keyed(18, []introspect.Column{{Key: "app.t.v", Type: "integer"}},
		[]introspect.Column{{Key: "app.t.v", Type: "bigint"}})
	target.Rewrites["column app.t.v"] = true
	target.Current.Objects = append(target.Current.Objects,
		introspect.Object{Key: "index app.t_v", SQL: "CREATE INDEX t_v ON app.t USING btree (v);"},
		introspect.Object{Key: "view app.tv", SQL: "CREATE VIEW app.tv AS SELECT t.v FROM app.t t;"})

	referenced := keyed(18, nil, nil)
	referenced.Rewrites["column app.t.v"] = true
	referenced.Current.Objects = append(referenced.Current.Objects, introspect.Object{
		Key: "constraint app.u.f", SQL: "ALTER TABLE app.u ADD CONSTRAINT f FOREIGN KEY (x) REFERENCES app.t(v);",
	})

	intents := plan.Intents{Retype: map[string]string{"column app.t.v": "v::bigint"}}

	checkPlans(t, []planCase{
		{
			name: "without rewrite", changes: []diff.Change{retype}, env: keyed(18, nil, nil),
			want: []string{"contract short: ALTER TABLE app.t ALTER COLUMN v TYPE bigint;"},
		},
		{name: "rewrite without intent", changes: []diff.Change{retype}, env: target},
		{name: "referenced by a foreign key", changes: []diff.Change{retype}, env: referenced, intents: intents},
		{
			name: "swapped", changes: []diff.Change{retype}, env: target, intents: intents,
			want: []string{
				"expand short: ALTER TABLE app.t ADD COLUMN v_ratel_new bigint; " +
					"CREATE FUNCTION app.t_v_ratel_new_ratel_sync() RETURNS trigger LANGUAGE plpgsql AS " +
					"$ratel$BEGIN NEW.v_ratel_new := NEW.v::bigint; RETURN NEW; END$ratel$; " +
					"CREATE TRIGGER t_v_ratel_new_ratel_sync BEFORE INSERT OR UPDATE ON app.t FOR EACH ROW " +
					"EXECUTE FUNCTION app.t_v_ratel_new_ratel_sync();",
				"expand long: CREATE INDEX CONCURRENTLY t_v_ratel_new ON app.t USING btree (v_ratel_new);",
				"backfill backfill: UPDATE app.t SET v_ratel_new = v::bigint WHERE v_ratel_new IS NULL AND v IS NOT NULL;",
				"contract short: DROP TRIGGER t_v_ratel_new_ratel_sync ON app.t; " +
					"DROP FUNCTION app.t_v_ratel_new_ratel_sync(); DROP VIEW app.tv; ALTER TABLE app.t DROP COLUMN v; " +
					"ALTER TABLE app.t RENAME COLUMN v_ratel_new TO v; ALTER INDEX app.t_v_ratel_new RENAME TO t_v; " +
					"CREATE VIEW app.tv AS SELECT t.v FROM app.t t;",
			},
		},
	})
}

func TestReplaceIdentifier(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ sql, want string }{
		{"v::bigint", "NEW.v::bigint"},
		{"lower(v) || 'v'", "lower(NEW.v) || 'v'"},
		{"t.v + vv + v_x", "t.NEW.v + vv + v_x"},
		{"'it''s v' || v", "'it''s v' || NEW.v"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			if got := plan.ReplaceIdentifier(tc.sql, "v", "NEW.v"); got != tc.want {
				t.Errorf("replaceIdentifier() = %q", got)
			}
		})
	}

	if plan.Mentions("SELECT 'v' FROM x", "v") || !plan.Mentions("SELECT v FROM x", "v") {
		t.Error("mentions() ignores literals and finds the identifier")
	}
}

func BenchmarkReplaceIdentifier(b *testing.B) {
	sql := strings.Repeat("lower(v) || 'v' || x.v, ", 20)

	for b.Loop() {
		plan.ReplaceIdentifier(sql, "v", "NEW.v")
	}
}
