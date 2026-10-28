package diff_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

func TestEnumLabels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		sql  string
		want []string
	}{
		{"CREATE TYPE app.e AS ENUM ();", nil},
		{"CREATE TYPE app.e AS ENUM ('a', 'b, c');", []string{"'a'", "'b, c'"}},
		{`CREATE TYPE app.e AS ENUM ('it''s', E'back\\slash');`, []string{"'it''s'", `E'back\\slash'`}},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			if difference := cmp.Diff(tc.want, diff.EnumLabels(tc.sql)); difference != "" {
				t.Errorf("enumLabels() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestAlteredEnum(t *testing.T) {
	t.Parallel()

	enum := func(labels string) string { return "CREATE TYPE app.e AS ENUM (" + labels + ");" }

	for _, tc := range []struct {
		name          string
		before, after string
		renamed       map[string]string
		want          []string
	}{
		{
			name: "added", before: enum("'b'"), after: enum("'a', 'b', 'c'"),
			want: []string{"ALTER TYPE app.e ADD VALUE 'a' BEFORE 'b';", "ALTER TYPE app.e ADD VALUE 'c' AFTER 'b';"},
		},
		{
			name: "first", before: enum(""), after: enum("'a'"),
			want: []string{"ALTER TYPE app.e ADD VALUE 'a';"},
		},
		{
			name: "renamed", before: enum("'a'"), after: enum("'z'"), renamed: map[string]string{"value app.e 'z'": "'a'"},
			want: []string{"ALTER TYPE app.e RENAME VALUE 'a' TO 'z';"},
		},
		{name: "removed", before: enum("'a', 'b'"), after: enum("'a'"), want: []string{""}},
		{name: "reordered", before: enum("'a', 'b'"), after: enum("'b', 'a'"), want: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			changes := diff.AlteredEnum(diff.Intents{Renamed: tc.renamed}, "type app.e", "app.e", tc.before, tc.after)
			if difference := cmp.Diff(tc.want, statements(changes)); difference != "" {
				t.Errorf("alteredEnum() (-want +got):\n%s", difference)
			}
		})
	}
}

func domains(domain introspect.Domain) introspect.Rendering {
	domain.Key = "type app.d"

	return introspect.Rendering{Domains: []introspect.Domain{domain}}
}

func TestAlteredType(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		current, desired introspect.Rendering
		before, after    string
		want             []string
	}{
		{
			name:   "domain in place",
			before: "CREATE DOMAIN app.d AS integer;", after: "CREATE DOMAIN app.d AS integer NOT NULL;",
			current: domains(introspect.Domain{
				Type: "integer", Default: "1", Checks: []introspect.Check{{Name: "a", Definition: "CHECK (VALUE > 0)"}},
			}),
			desired: domains(introspect.Domain{
				Type: "integer", NotNull: true, Checks: []introspect.Check{{Name: "b", Definition: "CHECK (VALUE > 1)"}},
			}),
			want: []string{
				"ALTER DOMAIN app.d DROP DEFAULT;", "ALTER DOMAIN app.d SET NOT NULL;",
				"ALTER DOMAIN app.d DROP CONSTRAINT a;", "ALTER DOMAIN app.d ADD CONSTRAINT b CHECK (VALUE > 1);",
			},
		},
		{
			name:   "domain defaults",
			before: "CREATE DOMAIN app.d AS integer NOT NULL;", after: "CREATE DOMAIN app.d AS integer;",
			current: domains(introspect.Domain{Type: "integer", NotNull: true}),
			desired: domains(introspect.Domain{Type: "integer", Default: "2"}),
			want:    []string{"ALTER DOMAIN app.d SET DEFAULT 2;", "ALTER DOMAIN app.d DROP NOT NULL;"},
		},
		{
			name: "domain base", before: "CREATE DOMAIN app.d AS integer;", after: "CREATE DOMAIN app.d AS bigint;",
			current: domains(introspect.Domain{Type: "integer"}), desired: domains(introspect.Domain{Type: "bigint"}),
			want: []string{"DROP DOMAIN app.d;", "CREATE DOMAIN app.d AS bigint;"},
		},
		{
			name: "composite", before: "CREATE TYPE app.d AS (a integer);", after: "CREATE TYPE app.d AS (a bigint);",
			want: []string{"DROP TYPE app.d;", "CREATE TYPE app.d AS (a bigint);"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			changes := diff.AlteredType(tc.current, tc.desired, diff.Intents{}, "type app.d", tc.before, tc.after)
			if difference := cmp.Diff(tc.want, statements(changes)); difference != "" {
				t.Errorf("alteredType() (-want +got):\n%s", difference)
			}
		})
	}
}

func TestAlteredDomain(t *testing.T) {
	t.Parallel()

	same := introspect.Domain{Type: "integer", Checks: []introspect.Check{{Name: "a", Definition: "CHECK (VALUE > 0)"}}}
	if changes := diff.AlteredDomain("type app.d", "app.d", same, same); len(changes) != 0 {
		t.Errorf("alteredDomain() of equal domains = %+v", changes)
	}
}

func TestDomainOf(t *testing.T) {
	t.Parallel()

	rendering := domains(introspect.Domain{Type: "integer"})

	for _, tc := range []struct {
		key  string
		want bool
	}{{"type app.d", true}, {"type app.x", false}} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()

			if ok := diff.DomainOf(rendering, tc.key); ok != tc.want {
				t.Errorf("domainOf(%q) = %v", tc.key, ok)
			}
		})
	}
}

func BenchmarkAlteredEnum(b *testing.B) {
	before := "CREATE TYPE app.e AS ENUM ('b', 'd', 'old');"
	after := "CREATE TYPE app.e AS ENUM ('a', 'b', 'c', 'd', 'new', 'e');"
	intents := diff.Intents{Renamed: map[string]string{"value app.e 'new'": "'old'"}}

	for b.Loop() {
		diff.AlteredEnum(intents, "type app.e", "app.e", before, after)
	}
}

func BenchmarkAlteredDomain(b *testing.B) {
	before := introspect.Domain{Type: "integer", Checks: []introspect.Check{{Name: "a", Definition: "CHECK (VALUE > 0)"}}}
	after := introspect.Domain{Type: "integer", NotNull: true, Checks: []introspect.Check{{Name: "b", Definition: "x"}}}

	for b.Loop() {
		diff.AlteredDomain("type app.d", "app.d", before, after)
	}
}
