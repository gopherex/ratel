package lint_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/lint"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

func TestNarrowingTypeChanges(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		before, after string
		narrows       bool
	}{
		{"bigint", "integer", true},
		{"integer", "bigint", false},
		{"double precision", "real", true},
		{"numeric", "numeric(10,2)", true},
		{"numeric(12,2)", "numeric(12,4)", true},
		{"numeric(10,2)", "numeric(12,2)", false},
		{"numeric(10,2)", "integer", true},
		{"integer", "numeric", false},
		{"text", "character varying(20)", true},
		{"character varying(20)", "character varying(30)", false},
		{`text COLLATE "C"`, "text", false},
		{"character varying(5)", "character", true},
		{"timestamp with time zone", "date", true},
		{"timestamp(6) without time zone", "timestamp(3) without time zone", true},
		{"date", "timestamp without time zone", false},
		{"text", "integer", false},
	} {
		t.Run(tc.before+" to "+tc.after, func(t *testing.T) {
			t.Parallel()

			changes := []diff.Change{{Kind: diff.AlterColumnType, Key: "column app.t.c", Before: tc.before, After: tc.after}}
			findings := lint.Changes(changes, plan.Intents{})

			if (len(findings) == 1) != tc.narrows {
				t.Errorf("Changes() = %v", findings)
			}
		})
	}
}

func TestNarrowingAllowed(t *testing.T) {
	t.Parallel()

	changes := []diff.Change{{Kind: diff.AlterColumnType, Key: "column app.t.c", Before: "bigint", After: "integer"}}
	findings := lint.Changes(changes, plan.Intents{Allow: map[string][]string{"column app.t.c": {"RTL-D03"}}})

	if len(findings) != 1 || !findings[0].Allowed {
		t.Errorf("Changes() = %v", findings)
	}
}

func BenchmarkChanges(b *testing.B) {
	changes := []diff.Change{{
		Kind: diff.AlterColumnType, Key: "column app.t.c",
		Before: "numeric(12,2)", After: "numeric(10,2)",
	}}

	for b.Loop() {
		lint.Changes(changes, plan.Intents{})
	}
}
