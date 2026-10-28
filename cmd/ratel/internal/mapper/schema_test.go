package mapper_test

import (
	"slices"
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func testSchemaDeclarations(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, sql := range []string{
		"CREATE SCHEMA app AUTHORIZATION owner",
		"CREATE EXTENSION hstore WITH SCHEMA app VERSION '1.8'",
		"CREATE POLICY own ON app.t AS RESTRICTIVE FOR SELECT TO reader USING (id > 0)",
		"GRANT SELECT, UPDATE ON app.t, app.u TO reader, PUBLIC WITH GRANT OPTION",
		"COMMENT ON COLUMN app.t.id IS 'identifier'",
	} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parser.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New(parser, "schema.sql", sql, raw, 0)
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

func testAlterTableFragments(t *testing.T, parser parse.Parser) {
	t.Helper()

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

			tree, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New(parser, "schema.sql", tc.sql, raw, 0)

			got := current.MapAlterTable(raw.GetStmt().GetAlterTableStmt())
			if got.GetRlsEnabled() != tc.enabled || got.GetRlsForced() != tc.forced ||
				len(got.GetConstraints()) != tc.constraints {
				t.Fatal(got)
			}
		})
	}
}

func testUnrepresentableGrants(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, sql := range []string{
		"GRANT SELECT(id) ON app.t TO reader", "REVOKE SELECT ON app.t FROM reader",
		"GRANT SELECT ON ALL TABLES IN SCHEMA app TO reader",
	} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parser.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New(parser, "schema.sql", sql, raw, 0)

			if got := current.MapGrant(raw.GetStmt().GetGrantStmt()); got != nil {
				t.Fatal("grant scope changed", got)
			}
		})
	}
}

func testTypeObjectReferences(t *testing.T, parser parse.Parser) {
	t.Helper()

	for _, sql := range []string{
		`COMMENT ON TYPE app.status IS 'state'`,
		`COMMENT ON DOMAIN app.status IS 'state'`,
		`GRANT USAGE ON TYPE app.status TO PUBLIC`,
		`GRANT USAGE ON DOMAIN app.status TO PUBLIC`,
	} {
		t.Run(sql, func(t *testing.T) {
			t.Parallel()

			parsed, err := parser.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := parsed.GetStmts()[0]
			current := mapper.New(parser, "type.sql", sql, raw, 0)
			node := raw.GetStmt()

			var reference *ir.ObjectRef
			if node.GetCommentStmt() != nil {
				reference, _ = current.MapComment(node.GetCommentStmt())
			} else {
				grants := current.MapGrant(node.GetGrantStmt())
				if len(grants) != 1 {
					t.Fatalf("grants = %v", grants)
				}

				reference = grants[0].GetObject()
			}

			if !slices.Equal(reference.GetName().GetParts(), []string{"app", "status"}) {
				t.Fatalf("reference lost identifier parts: %v", reference)
			}
		})
	}
}

func BenchmarkTypeObjectReferences(b *testing.B) {
	parser := newParser(b)

	sql := `COMMENT ON TYPE app.status IS 'state'`

	parsed, err := parser.Parse(sql)
	if err != nil {
		b.Fatal(err)
	}

	raw := parsed.GetStmts()[0]
	current := mapper.New(parser, "type.sql", sql, raw, 0)

	for b.Loop() {
		current.MapComment(raw.GetStmt().GetCommentStmt())
	}
}

func BenchmarkSchemaDeclarations(b *testing.B) {
	parser := newParser(b)

	sql := "CREATE POLICY own ON app.t FOR SELECT TO reader USING (id > 0)"

	tree, err := parser.Parse(sql)
	if err != nil {
		b.Fatal(err)
	}

	raw := tree.GetStmts()[0]
	current := mapper.New(parser, "bench.sql", sql, raw, 0)

	for b.Loop() {
		current.MapCreatePolicy(raw.GetStmt().GetCreatePolicyStmt())
	}
}
