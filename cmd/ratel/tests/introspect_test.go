//go:build integration

package tests_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

const introspectFixture = "../../../tests/testdata/catalog/schema.sql"

// TestIntrospectRoundTrip checks build(DDL) == introspect(apply(DDL)) per
// major. Facts only DDL carries (positions, annotations and what they derive:
// relations and annotation diagnostics) and the database name are cleared.
func TestIntrospectRoundTrip(t *testing.T) {
	t.Parallel()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		t.Fatal(err)
	}

	for _, fixture := range []struct{ path, namespace string }{
		{path: introspectFixture, namespace: "catalog_fixture"},
		{path: "testdata/introspect/schema.sql", namespace: "shop"},
	} {
		ddl, err := os.ReadFile(fixture.path)
		if err != nil {
			t.Fatal(err)
		}

		for _, major := range []int{16, 17, 18} {
			t.Run(fixture.namespace+"/"+strconv.Itoa(major), func(t *testing.T) {
				t.Parallel()
				checkRoundTrip(t, parser, string(ddl), fixture.namespace, major)
			})
		}
	}
}

func checkRoundTrip(t *testing.T, parser parse.Parser, ddl, namespace string, major int) {
	t.Helper()

	signatures, err := builtins.For(major)
	if err != nil {
		t.Fatal(err)
	}

	want, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: ddl}}, major, signatures)
	if err != nil {
		t.Fatal(err)
	}

	pool := postgresPool(t, major)

	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()

	// Roles are not schema objects; grants and policies need one to exist.
	if _, err := pool.Exec(ctx, "CREATE ROLE ratel_reader;\n"+ddl); err != nil {
		t.Fatal(err)
	}

	namespaces := []string{namespace}

	got, err := introspect.Load(ctx, parser, pool, namespaces)
	if err != nil {
		rendering, _ := introspect.Render(ctx, pool, namespaces)
		t.Fatalf("Load() error = %v\nrendered:\n%s", err, rendering.SQL)
	}

	normalize(want)
	normalize(got)
	pinExtensionVersions(want, got)
	dropDefaultGrants(want)

	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		rendering, _ := introspect.Render(ctx, pool, namespaces)
		t.Fatalf("introspected catalog differs (-build +introspect):\n%s\nrendered:\n%s", diff, rendering.SQL)
	}

	checkRenderIdempotent(ctx, t, pool, namespaces)
}

// checkRenderIdempotent applies the rendered DDL to an empty database and
// renders it again: the text is the same, so Render loses nothing it reads.
func checkRenderIdempotent(ctx context.Context, t *testing.T, pool *pgxpool.Pool, namespaces []string) {
	t.Helper()

	first, err := introspect.Render(ctx, pool, namespaces)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `CREATE DATABASE replica`); err != nil {
		t.Fatal(err)
	}

	config := pool.Config().ConnConfig.Copy()
	config.Database = "replica"

	replica, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = replica.Close(context.WithoutCancel(ctx)) }()

	if _, err := replica.Exec(ctx, "SET check_function_bodies = off;\n"+first.SQL); err != nil {
		t.Fatalf("apply rendered DDL: %v\n%s", err, first.SQL)
	}

	second, err := introspect.Render(ctx, replica, namespaces)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(first.SQL, second.SQL); diff != "" {
		t.Fatalf("render of the replica differs (-source +replica):\n%s", diff)
	}
}

// pinExtensionVersions: an extension declared without VERSION matches any
// installed version.
func pinExtensionVersions(want, got *ir.Catalog) {
	for _, declared := range want.GetExtensions() {
		for _, installed := range got.GetExtensions() {
			if declared.GetName() == installed.GetName() && declared.GetVersion() == "" {
				installed.Version = ""
			}
		}
	}
}

func TestIntrospectConnectionLoss(t *testing.T) {
	t.Parallel()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		t.Fatal(err)
	}

	pool := postgresPool(t, 18)

	ctx, cancel := context.WithTimeout(t.Context(), startupTimeout)
	defer cancel()

	if _, err := pool.Exec(ctx, `CREATE TABLE public.items (id integer)`); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE pid <> pg_backend_pid() AND backend_type = 'client backend'`); err != nil {
		t.Fatal(err)
	}

	pool.Close()

	got, err := introspect.Load(ctx, parser, pool, []string{"public"})
	if err == nil || got != nil {
		t.Fatalf("Load() on a closed pool = %v, %v; want no catalog and an error", got, err)
	}
}

// dropDefaultGrants removes grants of privileges PostgreSQL already gives
// PUBLIC on every type and routine; granting them changes nothing.
func dropDefaultGrants(result *ir.Catalog) {
	for _, schema := range result.GetSchemas() {
		grants := schema.GetGrants()[:0]

		for _, grant := range schema.GetGrants() {
			privilege := defaultPrivilege(grant.GetObject().GetKind())
			if privilege == "" || len(grant.GetPrivileges()) != 1 || grant.GetPrivileges()[0] != privilege ||
				len(grant.GetRoles()) != 1 || grant.GetRoles()[0] != "PUBLIC" || grant.GetWithGrantOption() {
				grants = append(grants, grant)
			}
		}

		schema.Grants = grants
	}
}

// defaultPrivilege is what acldefault grants PUBLIC on an object kind.
func defaultPrivilege(kind ir.ObjectKind) string {
	switch kind {
	case ir.ObjectKind_OBJECT_KIND_ENUM_TYPE, ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE,
		ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, ir.ObjectKind_OBJECT_KIND_RANGE_TYPE:
		return "USAGE"
	case ir.ObjectKind_OBJECT_KIND_FUNCTION, ir.ObjectKind_OBJECT_KIND_PROCEDURE:
		return "EXECUTE"
	default:
		return ""
	}
}

func normalize(result *ir.Catalog) {
	result.Name = ""
	result.Relations = nil
	result.Diagnostics = nil
	clearDDLOnly(result.ProtoReflect())
}

// clearDDLOnly also undoes PostgreSQL's coercion normalization: it stores
// expressions with every implicit coercion made explicit
// (nextval('s') -> nextval('s'::regclass), x < 1 -> x < (1)::numeric), so
// expressions are compared without casts.
func clearDDLOnly(message protoreflect.Message) {
	span := (&ir.SourceSpan{}).ProtoReflect().Descriptor().FullName()

	switch value := message.Interface().(type) {
	case *ir.Expr:
		for value.GetCast() != nil {
			value.Node = value.GetCast().GetExpr().GetNode()
		}
	case *ir.SelectStmt:
		// A query body's text is PostgreSQL's deparse; its structure is compared.
		value.RawSql = ""
	default:
	}

	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.Name() == "annotations":
			message.Clear(field)
		case field.Message() == nil || field.IsMap():
		case field.Message().FullName() == span:
			message.Clear(field)
		case field.IsList():
			for index := range value.List().Len() {
				clearDDLOnly(value.List().Get(index).Message())
			}
		default:
			clearDDLOnly(value.Message())
		}

		return true
	})
}
