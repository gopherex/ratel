package gengo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gopherex/ratel/pkg/plugin"
)

const protoTable = `CREATE SCHEMA shop;
-- @go.message example.com/shop/v1.Customer
-- @go.embed Address example.com/shop/v1.Address
CREATE TABLE shop.customers (
	-- @go.field Id value
	id bigint PRIMARY KEY,
	-- @go.field Status enum type=example.com/shop/v1.Status
	status text NOT NULL,
	-- @go.field Address.Street value
	home_street text,
	-- @go.field Contact.Phone value oneof=example.com/shop/v1.Customer_Phone case=phone
	phone text,
	-- @go.case Contact
	contact_case text
);
`

func BenchmarkProtoAnnotations(b *testing.B) {
	parser := newParser(b)

	path := filepath.Join(b.TempDir(), "schema.sql")
	if err := os.WriteFile(path, []byte(protoTable), 0o600); err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		if response := respond(b, parser, path); plugin.HasErrors(response) {
			b.Fatal(response.GetDiagnostics())
		}
	}
}

func TestProtoAnnotations(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	for _, tc := range []struct{ name, schema, failure string }{
		{name: "complete model", schema: protoTable},
		{
			name:    "field without message",
			schema:  strings.Replace(protoTable, "-- @go.message example.com/shop/v1.Customer\n", "", 1),
			failure: "has @go.field without @go.message",
		},
		{
			name:    "column without field",
			schema:  strings.Replace(protoTable, "\t-- @go.field Address.Street value\n", "", 1),
			failure: "has neither @go.field nor @go.case",
		},
		{
			name:    "unknown conversion",
			schema:  strings.Replace(protoTable, "Id value", "Id number", 1),
			failure: "@go.field takes",
		},
		{
			name:    "conversion without its type",
			schema:  strings.Replace(protoTable, " type=example.com/shop/v1.Status", "", 1),
			failure: "@go.field takes",
		},
		{
			name:    "type on a conversion without one",
			schema:  strings.Replace(protoTable, "Id value", "Id value type=int64", 1),
			failure: "@go.field takes",
		},
		{
			name:    "message on a column",
			schema:  strings.Replace(protoTable, "-- @go.field Id value", "-- @go.message example.com/shop/v1.Customer", 1),
			failure: "@go.message takes one Go type and targets a table",
		},
		{
			name:    "embed without its type",
			schema:  strings.Replace(protoTable, " example.com/shop/v1.Address", "", 1),
			failure: "@go.embed takes",
		},
		{
			name:    "member without its case",
			schema:  strings.Replace(protoTable, " case=phone", "", 1),
			failure: "@go.field takes",
		},
		{
			name:    "case with two paths",
			schema:  strings.Replace(protoTable, "@go.case Contact", "@go.case Contact Other", 1),
			failure: "@go.case takes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "schema.sql")
			if err := os.WriteFile(path, []byte(tc.schema), 0o600); err != nil {
				t.Fatal(err)
			}

			response := respond(t, parser, path)
			if tc.failure == "" {
				if plugin.HasErrors(response) {
					t.Fatalf("diagnostics = %v", response.GetDiagnostics())
				}

				return
			}

			if !plugin.HasErrors(response) || !strings.Contains(response.GetDiagnostics()[0].GetMessage(), tc.failure) ||
				response.GetDiagnostics()[0].GetCode() != "RTL-G03" {
				t.Fatalf("diagnostics = %v, want RTL-G03 %q", response.GetDiagnostics(), tc.failure)
			}
		})
	}
}
