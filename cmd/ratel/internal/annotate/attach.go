package annotate

import (
	"cmp"
	"slices"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

type locatedTarget struct {
	target Target
	start  int
	end    uint64
}

// Attach validates and attaches annotations to declarations in one source.
// Unowned plugin namespaces produce warnings; their syntax remains available.
func Attach(parser parse.Parser, file File, targets []Target, owned []string) ([]*ir.Diagnostic, error) {
	tokens, err := scanSQL(parser, file.Name, file.SQL)
	if err != nil {
		return nil, err
	}

	entries, err := scanAnnotations(file.Name, file.SQL, tokens)
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, nil
	}

	locations := locate(file, tokens, targets)

	var diagnostics []*ir.Diagnostic

	for _, entry := range entries {
		index := attachmentIndex(file.SQL, tokens, locations, entry)
		if index < 0 {
			return nil, invalid(entry.Value.GetSource(), "annotation has no adjacent target")
		}

		target := locations[index].target
		if err := validate(parser, entry.Value, target, false); err != nil {
			return nil, err
		}

		entry.Value.Target = &ir.AnnotationValue_Object{Object: target.Object}
		target.Meta.Annotations = append(target.Meta.GetAnnotations(), entry.Value)

		if warning := unowned(entry.Value, owned); warning != nil {
			diagnostics = append(diagnostics, warning)
		}
	}

	return diagnostics, nil
}

func unowned(value *ir.AnnotationValue, owned []string) *ir.Diagnostic {
	namespace := value.GetNamespace()
	if namespace == coreNamespace || contains(owned, namespace) {
		return nil
	}

	return &ir.Diagnostic{
		Severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_WARNING, Code: "RTL-A02",
		Message: "no configured plugin owns annotation namespace " + namespace, Source: value.GetSource(),
	}
}

func locate(file File, tokens []*pg.ScanToken, targets []Target) []locatedTarget {
	result := make([]locatedTarget, 0, len(targets))

	for _, target := range targets {
		source := target.Source
		if source == nil {
			source = target.Meta.GetSource()
		}

		if source == nil || source.GetFile() != file.Name {
			continue
		}

		for _, token := range tokens {
			start := token.GetStart()
			if start < 0 {
				continue
			}

			offset := uint64(start)
			if isComment(token) || token.GetToken() == pg.Token_ASCII_59 || offset < source.GetStartOffset() {
				continue
			}

			if offset >= source.GetEndOffset() {
				break
			}

			end := source.GetEndOffset()
			kind := target.Object.GetKind()

			if kind == ir.ObjectKind_OBJECT_KIND_COLUMN || kind == ir.ObjectKind_OBJECT_KIND_CONSTRAINT {
				end = elementEnd(tokens, start, end)
			}

			result = append(result, locatedTarget{target: target, start: int(start), end: end})

			break
		}
	}

	slices.SortFunc(result, func(left, right locatedTarget) int {
		if order := cmp.Compare(left.start, right.start); order != 0 {
			return order
		}

		return cmp.Compare(priority(left.target.Object.GetKind()), priority(right.target.Object.GetKind()))
	})

	return result
}

func priority(kind ir.ObjectKind) int {
	const (
		columnPriority = iota
		objectPriority
		constraintPriority
		syntheticPriority
	)

	switch kind {
	case ir.ObjectKind_OBJECT_KIND_COLUMN:
		return columnPriority
	case ir.ObjectKind_OBJECT_KIND_CONSTRAINT:
		return constraintPriority
	case ir.ObjectKind_OBJECT_KIND_SEQUENCE:
		return syntheticPriority
	default:
		return objectPriority
	}
}

func attachmentIndex(sql string, tokens []*pg.ScanToken, targets []locatedTarget, entry Entry) int {
	var previous, next *pg.ScanToken

	for _, token := range tokens {
		if isComment(token) {
			continue
		}

		if int(token.GetEnd()) <= entry.Start {
			previous = token
		}

		if int(token.GetStart()) >= entry.End {
			next = token

			break
		}
	}

	if previous != nil && !strings.Contains(sql[int(previous.GetEnd()):entry.Start], "\n") {
		if index := trailingTarget(targets, previous, entry); index >= 0 {
			return index
		}
	}

	if next != nil {
		for index, target := range targets {
			if target.start == int(next.GetStart()) {
				return index
			}
		}
	}

	return -1
}

func trailingTarget(targets []locatedTarget, previous *pg.ScanToken, entry Entry) int {
	if previous.GetToken() == pg.Token_ASCII_59 {
		return -1
	}

	chosen := -1

	for index, target := range targets {
		if entry.Start < 0 || target.start > int(previous.GetStart()) || target.end < uint64(entry.Start) {
			continue
		}

		kind := target.target.Object.GetKind()
		if kind != ir.ObjectKind_OBJECT_KIND_COLUMN && kind != ir.ObjectKind_OBJECT_KIND_CONSTRAINT {
			continue
		}

		if chosen < 0 || target.start > targets[chosen].start {
			chosen = index
		}
	}

	return chosen
}

func elementEnd(tokens []*pg.ScanToken, start int32, fallback uint64) uint64 {
	depth := 0
	comma := false

	for _, token := range tokens {
		offset := token.GetStart()
		if offset < start || offset < 0 || isComment(token) {
			continue
		}

		if comma {
			return uint64(offset)
		}

		switch token.GetToken() {
		case pg.Token_ASCII_40, pg.Token_ASCII_91:
			depth++
		case pg.Token_ASCII_41, pg.Token_ASCII_93:
			if depth == 0 {
				return uint64(offset)
			}

			depth--
		case pg.Token_ASCII_44:
			comma = depth == 0
		case pg.Token_ASCII_59:
			return min(fallback, uint64(offset))
		default:
		}
	}

	return fallback
}
