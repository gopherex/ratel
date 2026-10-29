package annotate

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

const (
	coreNamespace        = "ratel"
	relationAnnotation   = "rel"
	noRelationAnnotation = "norel"
	allowAnnotation      = "allow"
	renamedAnnotation    = "renamed"
	renamedValue         = "value"
	notNullAnnotation    = "notnull"
	nullableAnnotation   = "nullable"
)

// Target is a declared object eligible to receive source annotations.
type Target struct {
	Object     *ir.ObjectRef
	Meta       *ir.Metadata
	Source     *ir.SourceSpan
	ForeignKey bool
}

type rule struct {
	kinds              []ir.ObjectKind
	required, optional []string
	positional         bool
	repeat             bool
}

func coreRule(name string) (rule, bool) {
	column := ir.ObjectKind_OBJECT_KIND_COLUMN
	query := ir.ObjectKind_OBJECT_KIND_UNSPECIFIED

	switch name {
	case relationAnnotation:
		return rule{
			kinds:    []ir.ObjectKind{column, ir.ObjectKind_OBJECT_KIND_CONSTRAINT},
			required: []string{"name"}, optional: []string{"inverse"},
		}, true
	case noRelationAnnotation:
		return rule{kinds: []ir.ObjectKind{column, ir.ObjectKind_OBJECT_KIND_CONSTRAINT}}, true
	case renamedAnnotation:
		// With value= it renames one label of an enum, once per label.
		return rule{kinds: []ir.ObjectKind{
			ir.ObjectKind_OBJECT_KIND_TABLE, column, ir.ObjectKind_OBJECT_KIND_CONSTRAINT,
			ir.ObjectKind_OBJECT_KIND_INDEX, ir.ObjectKind_OBJECT_KIND_ENUM_TYPE, ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE,
			ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, ir.ObjectKind_OBJECT_KIND_RANGE_TYPE,
		}, required: []string{"from"}, optional: []string{renamedValue}, repeat: true}, true
	case "retype":
		return rule{kinds: []ir.ObjectKind{column}, required: []string{"using"}}, true
	case "backfill":
		return rule{kinds: []ir.ObjectKind{column}, required: []string{"expr"}, optional: []string{"batch"}}, true
	case allowAnnotation:
		return rule{required: []string{"reason"}, positional: true, repeat: true}, true
	case nullableAnnotation, notNullAnnotation:
		return rule{kinds: []ir.ObjectKind{query}, positional: true, repeat: true}, true
	case "row":
		return rule{kinds: []ir.ObjectKind{query}, positional: true}, true
	case "origin":
		return rule{kinds: []ir.ObjectKind{ir.ObjectKind_OBJECT_KIND_TABLE, column}, positional: true}, true
	default:
		return rule{}, false
	}
}

func validate(parser parse.Parser, value *ir.AnnotationValue, target Target, query bool) error {
	if value.GetNamespace() != coreNamespace {
		return nil
	}

	contract, ok := coreRule(value.GetName())
	if !ok {
		return invalid(value.GetSource(), "unknown core annotation "+value.GetName())
	}

	kind := target.Object.GetKind()
	if !allowedKind(contract.kinds, kind) || (query && value.GetName() == allowAnnotation) {
		return invalid(value.GetSource(), "annotation has wrong target")
	}

	if (value.GetName() == relationAnnotation || value.GetName() == noRelationAnnotation) && !target.ForeignKey {
		return invalid(value.GetSource(), "annotation target is not a foreign key")
	}

	if value.GetName() == renamedAnnotation && argument(value, renamedValue) != "" &&
		kind != ir.ObjectKind_OBJECT_KIND_ENUM_TYPE {
		return invalid(value.GetSource(), "value= renames a label of an enum type")
	}

	if err := validateArgs(parser, value, contract); err != nil {
		return err
	}

	return validateDuplicates(value, target.Meta.GetAnnotations(), contract.repeat)
}

func validateDuplicates(value *ir.AnnotationValue, previousValues []*ir.AnnotationValue, repeat bool) error {
	for _, previous := range previousValues {
		if previous.GetNamespace() != coreNamespace {
			continue
		}

		same := previous.GetName() == value.GetName()
		if same && (!repeat || argumentIdentity(previous) == argumentIdentity(value)) {
			message := "duplicate annotation; first declared at " + sourcePosition(previous.GetSource())

			return invalid(value.GetSource(), message)
		}

		if conflicting(previous, value) {
			return invalid(value.GetSource(), "conflicting annotations")
		}
	}

	return nil
}

func conflicting(left, right *ir.AnnotationValue) bool {
	switch left.GetName() {
	case relationAnnotation:
		return right.GetName() == noRelationAnnotation
	case noRelationAnnotation:
		return right.GetName() == relationAnnotation
	case nullableAnnotation:
		return right.GetName() == notNullAnnotation && argumentIdentity(left) == argumentIdentity(right)
	case notNullAnnotation:
		return right.GetName() == nullableAnnotation && argumentIdentity(left) == argumentIdentity(right)
	default:
		return false
	}
}

func allowedKind(kinds []ir.ObjectKind, kind ir.ObjectKind) bool {
	if len(kinds) == 0 {
		return kind != ir.ObjectKind_OBJECT_KIND_UNSPECIFIED
	}

	for _, candidate := range kinds {
		if candidate == kind {
			return true
		}
	}

	return false
}

func validateArgs(parser parse.Parser, value *ir.AnnotationValue, contract rule) error {
	present := make(map[string]string)
	positional := 0

	for _, argument := range value.GetArgs() {
		key, text := argument.GetKey(), argument.GetValue()
		if strings.TrimSpace(text) == "" {
			return invalid(value.GetSource(), "empty annotation value")
		}

		if key == "" {
			positional++
		} else if !contains(contract.required, key) && !contains(contract.optional, key) {
			return invalid(value.GetSource(), "unknown annotation key "+key)
		}

		present[key] = text
	}

	expected := 0
	if contract.positional {
		expected = 1
	}

	if positional != expected {
		return invalid(value.GetSource(), "wrong number of positional values")
	}

	for _, key := range contract.required {
		if _, ok := present[key]; !ok {
			return invalid(value.GetSource(), "missing annotation key "+key)
		}
	}

	return validateValues(parser, value, present)
}

func validateValues(parser parse.Parser, value *ir.AnnotationValue, args map[string]string) error {
	if batch, ok := args["batch"]; ok {
		number, err := strconv.ParseUint(batch, 10, 64)
		if err != nil || number == 0 {
			return invalid(value.GetSource(), "batch must be a positive integer")
		}
	}

	for _, key := range []string{"name", "inverse"} {
		if text, ok := args[key]; ok && !exported(text) {
			return invalid(value.GetSource(), "invalid relation name")
		}
	}

	switch value.GetName() {
	case "retype":
		return validateExpression(parser, value, args["using"])
	case "backfill":
		return validateExpression(parser, value, args["expr"])
	case "row":
		if !exported(args[""]) {
			return invalid(value.GetSource(), "invalid row name")
		}
	case allowAnnotation:
		if !validCode(args[""]) {
			return invalid(value.GetSource(), "invalid RTL code")
		}
	default:
	}

	return nil
}

func validCode(code string) bool {
	suffix, ok := strings.CutPrefix(code, "RTL-")
	if !ok || len(suffix) != len("L00") {
		return false
	}

	for index, char := range suffix {
		if index == 0 {
			if char < 'A' || char > 'Z' {
				return false
			}
		} else if char < '0' || char > '9' {
			return false
		}
	}

	return true
}

func validateExpression(parser parse.Parser, value *ir.AnnotationValue, text string) error {
	parsed, err := parser.Parse("SELECT (" + text + "\n)")
	if err != nil || len(parsed.GetStmts()) != 1 {
		return invalid(value.GetSource(), "invalid SQL expression")
	}

	statement := parsed.GetStmts()[0].GetStmt().GetSelectStmt()
	if !expressionStatement(statement) {
		return invalid(value.GetSource(), "invalid SQL expression")
	}

	return nil
}

func expressionStatement(statement *pg.SelectStmt) bool {
	return len(statement.GetTargetList()) == 1 && len(statement.GetFromClause()) == 0 &&
		statement.GetWhereClause() == nil && len(statement.GetGroupClause()) == 0 && statement.GetHavingClause() == nil &&
		len(statement.GetSortClause()) == 0 && statement.GetLimitCount() == nil && statement.GetLimitOffset() == nil &&
		len(statement.GetWindowClause()) == 0 && len(statement.GetLockingClause()) == 0 && statement.GetIntoClause() == nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}

	return false
}

// argumentIdentity tells repeatable annotations apart: by their positional
// value, a label rename by the label it names.
func argumentIdentity(value *ir.AnnotationValue) string {
	if value.GetName() == renamedAnnotation {
		return argument(value, renamedValue)
	}

	return argument(value, "")
}

// argument is the value of an annotation argument by key, empty when absent.
func argument(value *ir.AnnotationValue, key string) string {
	for _, current := range value.GetArgs() {
		if current.GetKey() == key {
			return current.GetValue()
		}
	}

	return ""
}

func sourcePosition(source *ir.SourceSpan) string {
	return source.GetFile() + ":" + strconv.FormatUint(uint64(source.GetStartLine()), 10) + ":" +
		strconv.FormatUint(uint64(source.GetStartColumn()), 10)
}

func exported(name string) bool {
	first, _ := utf8.DecodeRuneInString(name)

	return identifier(name) && unicode.IsUpper(first)
}
