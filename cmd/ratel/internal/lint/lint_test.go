package lint_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/lint"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

func TestPlannerAndFailed(t *testing.T) {
	t.Parallel()

	findings := lint.Planner([]plan.Finding{
		{Code: "RTL-L02", Change: "index app.i", Fixed: true},
		{Code: "RTL-R03", Change: "table app.t"},
		{Code: "RTL-L07", Change: "table app.c"},
		{Code: "RTL-D05", Change: "type app.e"},
	}, plan.Intents{Allow: map[string][]string{"table app.t": {"RTL-R03"}}},
		[]plan.Step{{Change: "table app.c", Allow: []string{"RTL-L07"}}})

	failed := lint.Failed(findings)
	if len(failed) != 1 || failed[0].Code != "RTL-D05" {
		t.Fatalf("Failed() = %v", failed)
	}

	lint.Sort(findings)

	want := []string{
		"index app.i fixed RTL-L02: ", "table app.c allowed RTL-L07: ", "table app.t allowed RTL-R03: ",
		"type app.e error RTL-D05: ",
	}
	for index, finding := range findings {
		if finding.String() != want[index] {
			t.Errorf("finding %d = %q", index, finding.String())
		}
	}
}

func BenchmarkFailed(b *testing.B) {
	findings := make([]lint.Finding, 100)
	for index := range findings {
		findings[index] = lint.Finding{Code: "RTL-L02", Fixed: index%2 == 0}
	}

	for b.Loop() {
		lint.Failed(findings)
	}
}
