package gengo_test

import (
	"slices"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
)

func TestScalar(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, want, imported string
		ok                   bool
	}{
		{name: "int8", want: "int64", ok: true},
		{name: "numeric", want: "decimal.Decimal", imported: "github.com/shopspring/decimal", ok: true},
		{name: "uuid", want: "uuid.UUID", imported: "github.com/google/uuid", ok: true},
		{name: "timestamptz", want: "time.Time", imported: "time", ok: true},
		{name: "inet", want: "netip.Addr", imported: "net/netip", ok: true},
		{name: "cidr", want: "netip.Prefix", imported: "net/netip", ok: true},
		{name: "interval", want: "pgtype.Interval", imported: "github.com/jackc/pgx/v5/pgtype", ok: true},
		{name: "int4range", want: "pgtype.Range[int32]", imported: "github.com/jackc/pgx/v5/pgtype", ok: true},
		{name: "tstzmultirange", want: "pgtype.Multirange[pgtype.Range[time.Time]]", imported: "time", ok: true},
		{name: "jsonb", want: "json.RawMessage", imported: "encoding/json", ok: true},
		{name: "tsvector"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := gengo.Scalar(tc.name)
			if ok != tc.ok || got.Expr != tc.want || (tc.imported != "" && !slices.Contains(got.Imports, tc.imported)) {
				t.Fatalf("Scalar(%q) = %+v, %v", tc.name, got, ok)
			}
		})
	}
}

func TestOverride(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want, imported string }{
		{in: "string", want: "string"},
		{in: "github.com/acme/md.Markdown", want: "md.Markdown", imported: "github.com/acme/md"},
		{in: "time.Duration", want: "time.Duration", imported: "time"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			got := gengo.Override(tc.in)
			if got.Expr != tc.want || (tc.imported != "" && !slices.Equal(got.Imports, []string{tc.imported})) {
				t.Fatalf("Override(%q) = %+v", tc.in, got)
			}
		})
	}
}

func BenchmarkScalar(b *testing.B) {
	for b.Loop() {
		gengo.Scalar("tstzmultirange")
	}
}
