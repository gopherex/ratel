package lint

import (
	"slices"
	"strconv"
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

// Changes lints what a plan changes: type changes that narrow a column.
func Changes(changes []diff.Change, intents plan.Intents) []Finding {
	var findings []Finding

	for index := range changes {
		change := &changes[index]
		if change.Kind != diff.AlterColumnType || !narrows(baseType(change.Before), baseType(change.After)) {
			continue
		}

		findings = append(findings, Finding{
			Code: "RTL-D03", Where: change.Key, Detail: detail("RTL-D03") + ": " + change.Before + " to " + change.After,
			Allowed: slices.Contains(intents.Allow[change.Key], "RTL-D03"),
		})
	}

	return findings
}

// baseType is a rendered column type without its collation clause.
func baseType(rendered string) string {
	base, _, _ := strings.Cut(rendered, " COLLATE ")

	return base
}

// narrows reports a type change that may lose values: a smaller number,
// a shorter string, a lost fraction or time of day.
func narrows(before, after string) bool {
	switch beforeFamily, afterFamily := family(before), family(after); {
	case beforeFamily == numeric && afterFamily == numeric:
		return numericNarrows(before, after)
	case beforeFamily == text && afterFamily == text:
		return length(after) < length(before)
	case beforeFamily == temporal && afterFamily == temporal:
		return temporalRank(after) < temporalRank(before) || precision(after) < precision(before)
	default:
		return false
	}
}

const (
	other = iota
	numeric
	text
	temporal
)

func family(name string) int {
	switch {
	case slices.Contains([]string{"smallint", "integer", "bigint", "real", "double precision"}, name),
		strings.HasPrefix(name, "numeric"):
		return numeric
	case name == "text" || strings.HasPrefix(name, "character"):
		return text
	case name == dateType || strings.HasPrefix(name, "timestamp"):
		return temporal
	default:
		return other
	}
}

// numericNarrows compares integer width, floating point and the digits of
// numeric(p, s) before and after the decimal point.
func numericNarrows(before, after string) bool {
	beforeRank, afterRank := numericRank(before), numericRank(after)
	if beforeRank != afterRank {
		return afterRank < beforeRank
	}

	if !strings.HasPrefix(before, "numeric") {
		return false
	}

	was, is := numericDigits(before), numericDigits(after)

	return is.fraction < was.fraction || is.whole < was.whole
}

// numericRank orders numeric types by the values they hold; an unbounded
// numeric holds every one.
func numericRank(name string) int {
	ranks := []string{"smallint", "integer", "bigint", "real", "double precision"}
	if rank := slices.Index(ranks, name); rank >= 0 {
		return rank
	}

	return len(ranks)
}

// dateType is the temporal type without a time of day.
const dateType = "date"

// unbounded stands for a missing length or precision.
const unbounded = 1 << 30

// digits are the digits a numeric holds before and after the decimal point.
type digits struct{ whole, fraction int }

func numericDigits(name string) digits {
	inner, ok := modifier(name)
	if !ok {
		return digits{whole: unbounded, fraction: unbounded}
	}

	precision, scale, _ := strings.Cut(inner, ",")
	total, _ := strconv.Atoi(strings.TrimSpace(precision))
	fraction, _ := strconv.Atoi(strings.TrimSpace(scale))

	return digits{whole: total - fraction, fraction: fraction}
}

func length(name string) int {
	if inner, ok := modifier(name); ok {
		value, _ := strconv.Atoi(inner)

		return value
	}

	if name == "character" {
		return 1
	}

	return unbounded
}

func temporalRank(name string) int {
	if name == dateType {
		return 0
	}

	return 1
}

// defaultPrecision is the fractional seconds of an unmodified timestamp.
const defaultPrecision = 6

func precision(name string) int {
	if inner, ok := modifier(name); ok {
		value, _ := strconv.Atoi(inner)

		return value
	}

	if name == dateType {
		return 0
	}

	return defaultPrecision
}

// modifier is the text between the parentheses of a type name.
func modifier(name string) (string, bool) {
	_, rest, ok := strings.Cut(name, "(")
	if !ok {
		return "", false
	}

	inner, _, _ := strings.Cut(rest, ")")

	return inner, true
}
