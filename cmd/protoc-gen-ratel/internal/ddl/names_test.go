package ddl_test

import (
	"errors"
	"testing"

	"github.com/gopherex/ratel/cmd/protoc-gen-ratel/internal/ddl"
)

func TestSnakeCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"user", "user"},
		{"UserAccount", "user_account"},
		{"UserID", "user_id"},
		{"HTTPServer", "http_server"},
		{"Order2Item", "order2_item"},
		{"already_snake", "already_snake"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			if got := ddl.SnakeCase(tc.in); got != tc.want {
				t.Errorf("SnakeCase(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestQuoteIdentAndSQLString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"identifier", ddl.QuoteIdent("user"), `"user"`},
		{"identifier with quote", ddl.QuoteIdent(`a"b`), `"a""b"`},
		{"string literal", ddl.SQLString("ORDER_KIND_WEB"), `'ORDER_KIND_WEB'`},
		{"string literal with quote", ddl.SQLString("it's"), `'it''s'`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.got != tc.want {
				t.Errorf("got %s, want %s", tc.got, tc.want)
			}
		})
	}
}

func TestExported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want bool
	}{
		{"Customer", true},
		{"Ünit", true},
		{"", false},
		{"customer", false},
		{"Bad-Name", false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			if got := ddl.Exported(tc.in); got != tc.want {
				t.Errorf("Exported(%q) = %t, want %t", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateExpr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"simple", "now()", false},
		{"comparison", "tenant_no > 0", false},
		{"empty", "", true},
		{"statement end", "1); DROP TABLE x", true},
		{"line comment", "1 -- x", true},
		{"block comment", "1 /* x */", true},
		{"newline", "1\n", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ddl.ValidateExpr(tc.expr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateExpr(%q) error = %v, want error %t", tc.expr, err, tc.wantErr)
			}

			if err != nil && !errors.Is(err, ddl.ErrInvalidOption) {
				t.Errorf("ValidateExpr(%q) error = %v, want ErrInvalidOption", tc.expr, err)
			}
		})
	}
}

func BenchmarkSnakeCase(b *testing.B) {
	for b.Loop() {
		_ = ddl.SnakeCase("HTTPServerRequestID")
	}
}
