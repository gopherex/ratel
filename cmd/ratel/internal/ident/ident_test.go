package ident_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/ident"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func newQuoter(tb testing.TB) *ident.Quoter {
	tb.Helper()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		tb.Fatal(err)
	}

	return ident.New(parser)
}

func TestQuote(t *testing.T) {
	quoter := newQuoter(t)

	t.Parallel()

	for _, tc := range []struct{ name, want string }{
		{"users", "users"},
		{"user_2$", "user_2$"},
		{"Users", `"Users"`},
		{"order", `"order"`},
		{"name", "name"},
		{"2fa", `"2fa"`},
		{`say"hi`, `"say""hi"`},
		{"", `""`},
	} {
		if got := quoter.Quote(tc.name); got != tc.want {
			t.Errorf("Quote(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	if got := quoter.Qualified("app", "Order", "id"); got != `app."Order".id` {
		t.Errorf("Qualified() = %q", got)
	}
}

func BenchmarkQuote(b *testing.B) {
	quoter := newQuoter(b)

	for b.Loop() {
		quoter.Quote("order")
	}
}
