package mapper_test

import (
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestSchemaDeclarations(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, sql := range []string{
		"CREATE SCHEMA app AUTHORIZATION owner",
		"CREATE EXTENSION hstore WITH SCHEMA app VERSION '1.8'",
		"CREATE POLICY own ON app.t AS RESTRICTIVE FOR SELECT TO reader USING (id > 0)",
		"GRANT SELECT, UPDATE ON app.t, app.u TO reader, PUBLIC WITH GRANT OPTION",
		"COMMENT ON COLUMN app.t.id IS 'identifier'",
	} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parse.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New("schema.sql", sql, raw, 0)
			node := raw.GetStmt()

			switch {
			case node.GetCreateSchemaStmt() != nil:
				got := current.MapCreateSchema(node.GetCreateSchemaStmt())
				if got.GetName() != "app" || got.GetOwner() != "owner" {
					t.Fatal(got)
				}
			case node.GetCreateExtensionStmt() != nil:
				got := current.MapCreateExtension(node.GetCreateExtensionStmt())
				if got.GetSchema() != "app" || got.GetVersion() != "1.8" {
					t.Fatal(got)
				}
			case node.GetCreatePolicyStmt() != nil:
				got := current.MapCreatePolicy(node.GetCreatePolicyStmt())
				checkPolicy(t, got)
			case node.GetGrantStmt() != nil:
				got := current.MapGrant(node.GetGrantStmt())
				if len(got) != 2 {
					t.Fatal(got)
				}

				checkGrant(t, got[0])
			case node.GetCommentStmt() != nil:
				object, metadata := current.MapComment(node.GetCommentStmt())
				if len(object.GetName().GetParts()) != 3 || metadata.GetComment() != "identifier" {
					t.Fatal(object, metadata)
				}
			}
		})
	}
}

func checkPolicy(t *testing.T, policy *ir.Policy) {
	t.Helper()

	if !policy.GetRestrictive() || policy.GetUsing() == nil || len(policy.GetRoles()) != 1 {
		t.Fatal(policy)
	}
}

func checkGrant(t *testing.T, grant *ir.Grant) {
	t.Helper()

	if len(grant.GetPrivileges()) != 2 || len(grant.GetRoles()) != 2 {
		t.Fatal(grant)
	}
}

func TestAlterTableFragments(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct {
		sql             string
		enabled, forced bool
		constraints     int
	}{
		{"ALTER TABLE app.t ADD CONSTRAINT positive CHECK (id>0) NOT VALID", false, false, 1},
		{"ALTER TABLE app.t ENABLE ROW LEVEL SECURITY", true, false, 0},
		{"ALTER TABLE app.t FORCE ROW LEVEL SECURITY", false, true, 0},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New("schema.sql", tc.sql, raw, 0)

			got := current.MapAlterTable(raw.GetStmt().GetAlterTableStmt())
			if got.GetRlsEnabled() != tc.enabled || got.GetRlsForced() != tc.forced ||
				len(got.GetConstraints()) != tc.constraints {
				t.Fatal(got)
			}
		})
	}
}

func TestUnrepresentableGrants(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, sql := range []string{
		"GRANT SELECT(id) ON app.t TO reader", "REVOKE SELECT ON app.t FROM reader",
		"GRANT SELECT ON ALL TABLES IN SCHEMA app TO reader",
	} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parse.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New("schema.sql", sql, raw, 0)

			if got := current.MapGrant(raw.GetStmt().GetGrantStmt()); got != nil {
				t.Fatal("grant scope changed", got)
			}
		})
	}
}

func BenchmarkSchemaDeclarations(b *testing.B) {
	sql := "CREATE POLICY own ON app.t FOR SELECT TO reader USING (id > 0)"

	tree, err := parse.Parse(sql)
	if err != nil {
		b.Fatal(err)
	}

	raw := tree.GetStmts()[0]
	current := mapper.New("bench.sql", sql, raw, 0)

	for b.Loop() {
		current.MapCreatePolicy(raw.GetStmt().GetCreatePolicyStmt())
	}
}
