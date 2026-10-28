package catalog_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
)

const usersTable = "CREATE TABLE users(id int PRIMARY KEY); "

func TestRelations(t *testing.T) {
	t.Parallel()

	parser := newParser(t)

	for _, tc := range []struct {
		name      string
		sql       string
		relations []string
		codes     []string
	}{
		{
			name:      "foreign key",
			sql:       usersTable + "CREATE TABLE posts(id int PRIMARY KEY, author_id int NOT NULL REFERENCES users(id));",
			relations: []string{"Author/Posts ONE/MANY public.posts>public.users"},
		},
		{
			name:      "unique foreign key has to-one inverse",
			sql:       usersTable + "CREATE TABLE profiles(id int PRIMARY KEY, user_id int UNIQUE REFERENCES users(id));",
			relations: []string{"User/Profiles ONE/ONE public.profiles>public.users"},
		},
		{
			name: "norel suppresses",
			sql: usersTable + "CREATE TABLE posts(id int PRIMARY KEY,\n" +
				" -- @ratel.norel\n author_id int REFERENCES users(id));",
		},
		{
			name: "rel overrides names",
			sql: usersTable + "CREATE TABLE posts(id int PRIMARY KEY,\n" +
				" -- @ratel.rel name=Writer inverse=Articles\n author_id int REFERENCES users(id));",
			relations: []string{"Writer/Articles ONE/MANY public.posts>public.users"},
		},
		{
			name: "rel on constraint",
			sql: usersTable + "CREATE TABLE posts(id int PRIMARY KEY, author_id int,\n" +
				" -- @ratel.rel name=Writer\n CONSTRAINT posts_author FOREIGN KEY (author_id) REFERENCES users(id));",
			relations: []string{"Writer/Posts ONE/MANY public.posts>public.users"},
		},
		{
			name: "same pair needs rel",
			sql: usersTable + "CREATE TABLE posts(id int PRIMARY KEY, author_id int REFERENCES users(id), " +
				"editor_id int REFERENCES users(id));",
			relations: []string{
				"Author/Posts ONE/MANY public.posts>public.users",
				"Editor/Posts ONE/MANY public.posts>public.users",
			},
			codes: []string{"RTL-C05", "RTL-C05", "RTL-C08"},
		},
		{
			name: "same pair with rel on both",
			sql: usersTable + "CREATE TABLE posts(id int PRIMARY KEY,\n" +
				" -- @ratel.rel name=Author inverse=Authored\n author_id int REFERENCES users(id),\n" +
				" -- @ratel.rel name=Editor inverse=Edited\n editor_id int REFERENCES users(id));",
			relations: []string{
				"Author/Authored ONE/MANY public.posts>public.users",
				"Editor/Edited ONE/MANY public.posts>public.users",
			},
		},
		{
			name: "join table",
			sql: usersTable + "CREATE TABLE roles(id int PRIMARY KEY); " +
				"CREATE TABLE user_roles(user_id int REFERENCES users(id), role_id int REFERENCES roles(id), " +
				"PRIMARY KEY (user_id, role_id));",
			relations: []string{
				"Role/UserRoles ONE/MANY public.user_roles>public.roles",
				"Users/Roles MANY/MANY public.roles>public.users via public.user_roles",
				"User/UserRoles ONE/MANY public.user_roles>public.users",
			},
		},
		{
			name: "join table with norel key has no many-to-many",
			sql: usersTable + "CREATE TABLE roles(id int PRIMARY KEY); " +
				"CREATE TABLE user_roles(\n -- @ratel.norel\n user_id int REFERENCES users(id), " +
				"role_id int REFERENCES roles(id), PRIMARY KEY (user_id, role_id));",
			relations: []string{"Role/UserRoles ONE/MANY public.user_roles>public.roles"},
		},
		{
			name: "join name annotation",
			sql: usersTable + "CREATE TABLE roles(id int PRIMARY KEY); " +
				"CREATE TABLE user_roles(\n -- @ratel.rel name=Members\n user_id int REFERENCES users(id), " +
				"role_id int REFERENCES roles(id), PRIMARY KEY (user_id, role_id));",
			relations: []string{
				"Role/UserRoles ONE/MANY public.user_roles>public.roles",
				"Members/Roles MANY/MANY public.roles>public.users via public.user_roles",
				"Members/UserRoles ONE/MANY public.user_roles>public.users",
			},
		},
		{
			name: "column collision",
			sql: usersTable + "CREATE TABLE posts(id int PRIMARY KEY, author int, " +
				"author_id int REFERENCES users(id));",
			relations: []string{"Author/Posts ONE/MANY public.posts>public.users"},
			codes:     []string{"RTL-C08"},
		},
		{
			name:      "underivable name",
			sql:       usersTable + `CREATE TABLE posts(id int PRIMARY KEY, "9x_id" int REFERENCES users(id));`,
			relations: []string{"9x/Posts ONE/MANY public.posts>public.users"},
			codes:     []string{"RTL-C07"},
		},
		{
			name: "conflicting annotations",
			sql: usersTable + "CREATE TABLE posts(id int PRIMARY KEY,\n -- @ratel.rel name=Writer\n author_id int,\n" +
				" -- @ratel.rel name=Owner\n CONSTRAINT posts_author FOREIGN KEY (author_id) REFERENCES users(id));",
			codes: []string{"RTL-C06"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: tc.sql}}, 18, nil)
			if err != nil {
				t.Fatal(err)
			}

			if got := relationSummary(result); !slices.Equal(got, tc.relations) {
				t.Fatalf("relations = %q, want %q", got, tc.relations)
			}

			if got := errorCodes(result); !slices.Equal(got, tc.codes) {
				t.Fatalf("codes = %q, want %q", got, tc.codes)
			}
		})
	}
}

func BenchmarkRelations(b *testing.B) {
	parser := newParser(b)

	var sql strings.Builder

	sql.WriteString("CREATE TABLE hub(id int PRIMARY KEY); ")

	for index := range 300 {
		fmt.Fprintf(&sql, "CREATE TABLE t%d(id int PRIMARY KEY, hub_id int REFERENCES hub(id), "+
			"other_id int REFERENCES t%d_ref(id)); CREATE TABLE t%d_ref(id int PRIMARY KEY); ", index, index, index)
	}

	files := []catalog.File{{Name: "bench.sql", SQL: sql.String()}}

	for b.Loop() {
		if _, err := catalog.Build(parser, files, 18, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func relationSummary(c *ir.Catalog) []string {
	result := make([]string, 0, len(c.GetRelations()))

	for _, relation := range c.GetRelations() {
		line := fmt.Sprintf("%s/%s %s/%s %s>%s", relation.GetName(), relation.GetInverseName(),
			kindName(relation.GetKind()), kindName(relation.GetInverseKind()),
			catalog.ObjectID(relation.GetFrom().GetTable()), catalog.ObjectID(relation.GetTo().GetTable()))

		if relation.GetThrough() != nil {
			line += " via " + catalog.ObjectID(relation.GetThrough())
		}

		result = append(result, line)
	}

	return result
}

func kindName(kind ir.RelationKind) string {
	return strings.TrimPrefix(kind.String(), "RELATION_KIND_")
}

func errorCodes(c *ir.Catalog) []string {
	var codes []string

	for _, diagnostic := range c.GetDiagnostics() {
		if diagnostic.GetSeverity() == ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR {
			codes = append(codes, diagnostic.GetCode())
		}
	}

	return codes
}
