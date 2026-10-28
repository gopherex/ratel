// Package lint applies the RTL-* rules of docs/migrations.md to migration
// steps, to the changes a plan makes and to the desired schema. A finding
// fails lint unless the planner fixed it or the step or object allows its
// code with @ratel.allow.
package lint

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

// Finding is one rule broken at one place.
type Finding struct {
	Code  string
	Where string // "file step-id", a change key or an object
	// Detail says what broke the rule, or how the planner fixed it.
	Detail string
	// Fixed is a finding the planner's recipe avoided; Allowed one that
	// @ratel.allow permits. Neither fails lint.
	Fixed, Allowed bool
}

func (f Finding) String() string {
	status := "error"

	switch {
	case f.Fixed:
		status = "fixed"
	case f.Allowed:
		status = "allowed"
	default:
	}

	return fmt.Sprintf("%s %s %s: %s", f.Where, status, f.Code, f.Detail)
}

// Failed are the findings that fail lint.
func Failed(findings []Finding) []Finding {
	var failed []Finding

	for _, finding := range findings {
		if !finding.Fixed && !finding.Allowed {
			failed = append(failed, finding)
		}
	}

	return failed
}

// Planner turns the planner's findings into lint findings, allowed when the
// change's object allows the code.
func Planner(findings []plan.Finding, intents plan.Intents, steps []plan.Step) []Finding {
	result := make([]Finding, 0, len(findings))

	for _, finding := range findings {
		allowed := slices.Contains(intents.Allow[finding.Change], finding.Code)

		for index := range steps {
			if steps[index].Change == finding.Change && slices.Contains(steps[index].Allow, finding.Code) {
				allowed = true
			}
		}

		result = append(result, Finding{
			Code: finding.Code, Where: finding.Change, Detail: finding.Detail, Fixed: finding.Fixed, Allowed: allowed,
		})
	}

	return result
}

// Sort orders findings by place, then code.
func Sort(findings []Finding) {
	slices.SortStableFunc(findings, func(a, b Finding) int {
		if order := strings.Compare(a.Where, b.Where); order != 0 {
			return order
		}

		return strings.Compare(a.Code, b.Code)
	})
}
