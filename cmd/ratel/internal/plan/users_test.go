package plan_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

func TestUsersRecreated(t *testing.T) {
	t.Parallel()

	views := []introspect.Object{
		{Key: "view app.a", SQL: "CREATE VIEW app.a AS SELECT 1 AS x;"},
		{Key: "view app.b", SQL: "CREATE VIEW app.b AS SELECT x FROM app.a;"},
		{Key: "view app.c", SQL: "CREATE VIEW app.c AS SELECT x FROM app.b;"},
	}
	replace := []diff.Change{
		diff.Dropped(views[0]),
		diff.Created(introspect.Object{Key: "view app.a", SQL: "CREATE VIEW app.a AS SELECT 'y' AS x;"}),
	}

	chain := env(18, "app.a", "app.b", "app.c")
	chain.Current.Objects = views
	chain.Desired.Objects = views
	chain.Dependents = map[string][]string{"view app.a": {"view app.b"}, "view app.b": {"view app.c"}}

	column := env(18, "app.a")
	column.Dependents = map[string][]string{"view app.a": {"column app.t.x"}}

	checkPlans(t, []planCase{
		{
			name: "views in a chain", changes: replace, env: chain,
			want: []string{
				"contract short: DROP VIEW app.c; DROP VIEW app.b; DROP VIEW app.a; " +
					"CREATE VIEW app.a AS SELECT 'y' AS x; CREATE VIEW app.b AS SELECT x FROM app.a; " +
					"CREATE VIEW app.c AS SELECT x FROM app.b;",
			},
		},
		{name: "used by a column", changes: replace, env: column},
	})
}
