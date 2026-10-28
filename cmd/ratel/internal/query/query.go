// Package query infers complete named-query signatures from PostgreSQL syntax.
package query

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/annotate"
	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

const (
	nullableAnnotation = "nullable"
	notnullAnnotation  = "notnull"
)

// Error refuses an incomplete or inconsistent query signature.
type Error struct{ Diagnostic *ir.Diagnostic }

func (e *Error) Error() string {
	source := e.Diagnostic.GetSource()

	return fmt.Sprintf("%s:%d:%d: %s: %s", source.GetFile(), source.GetStartLine(), source.GetStartColumn(),
		e.Diagnostic.GetCode(), e.Diagnostic.GetMessage())
}

type inference struct {
	parser     parse.Parser
	catalog    *ir.Catalog
	signatures signatures
	query      *ir.Query
	params     map[int32]*ir.QueryParameter
	contextual map[int32]bool
	refTypes   map[*pg.ParamRef]*ir.TypeRef
	refs       []*pg.ParamRef
	names      map[int32]string
	qualified  bool
	err        error
}

// Infer never mutates its inputs. Builtins must match the catalog's target major.
// Output order follows sources; parameters follow their contiguous $N positions.
func Infer(
	parser parse.Parser, cat *ir.Catalog, builtins []*ir.Schema, sources []*annotate.QuerySource,
) ([]*ir.Query, error) {
	signatures := newSignatures(cat, builtins)
	result := make([]*ir.Query, 0, len(sources))
	ordinals := make(map[string]int)

	for _, source := range sources {
		state := inference{
			parser: parser, catalog: cat, signatures: signatures,
			params: make(map[int32]*ir.QueryParameter), contextual: make(map[int32]bool),
			refTypes: make(map[*pg.ParamRef]*ir.TypeRef),
		}

		file := source.Meta.GetSource().GetFile()
		inferred, err := state.infer(source, ordinals[file])
		ordinals[file]++

		if err != nil {
			return nil, err
		}

		result = append(result, inferred)
	}

	return result, nil
}

func (s *inference) fail(expression string) {
	if s.err == nil {
		s.err = &Error{Diagnostic: &ir.Diagnostic{
			Severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR, Code: "RTL-Q01",
			Message: "query " + s.query.GetName() + ": cannot infer " + expression, Source: s.query.GetMeta().GetSource(),
		}}
	}
}

func (s *inference) infer(source *annotate.QuerySource, ordinal int) (*ir.Query, error) {
	s.query = &ir.Query{Name: source.Name, Command: source.Command, Meta: proto.CloneOf(source.Meta)}

	rewritten, err := rewrite(s.parser, source.SQL, s.signatures.operators)
	if err != nil {
		return nil, err
	}

	if rewritten.mixed {
		s.fail("mixed named and positional parameters")

		return nil, s.err
	}

	s.names = rewritten.names
	sql := rewritten.sql
	s.query.Sql = strings.TrimSuffix(strings.TrimSpace(sql), ";")

	tree, err := s.parser.Parse(sql)
	if err != nil {
		return nil, err
	}

	if len(tree.GetStmts()) != 1 {
		s.fail("statement (expected exactly one)")

		return nil, s.err
	}

	raw := tree.GetStmts()[0]
	walkMessages(raw.GetStmt().ProtoReflect(), func(message proto.Message) {
		switch ref := message.(type) {
		case *pg.ParamRef:
			s.parameterValue(ref)
		default:
		}
	})

	s.query.Columns = s.statement(raw.GetStmt(), newScope(nil))
	// A later occurrence can constrain an earlier result parameter.
	if s.err == nil {
		s.query.Columns = s.statement(raw.GetStmt(), newScope(nil))
	}

	s.finishSignature()

	s.annotations()

	if s.err != nil {
		return nil, s.err
	}

	if err := s.mapQuery(source, tree, sql, ordinal); err != nil {
		return nil, err
	}

	return s.query, nil
}

func (s *inference) finishSignature() {
	for number := 1; number <= len(s.params); number++ {
		param := s.params[int32(number)]
		if param == nil || !completeType(param.GetType()) {
			s.fail("parameter $" + strconv.Itoa(number))

			continue
		}

		param.Name = s.names[int32(number)]
		s.query.Parameters = append(s.query.GetParameters(), param)
	}

	for _, ref := range s.refs {
		if s.refTypes[ref] == nil {
			s.fail("parameter $" + strconv.Itoa(int(ref.GetNumber())))
		}
	}

	for _, column := range s.query.GetColumns() {
		if !completeType(column.GetType()) {
			s.fail("result expression " + column.GetName())
		}
	}
}

func (s *inference) mapQuery(source *annotate.QuerySource, tree *pg.ParseResult, sql string, ordinal int) error {
	if s.qualified {
		canonical, err := s.parser.Deparse(tree)
		if err != nil {
			return err
		}

		s.query.Sql = canonical
	}

	raw := tree.GetStmts()[0]
	mapped := mapper.New(s.parser, source.Meta.GetSource().GetFile(), sql, raw, ordinal)
	s.query.Ast = mapped.MapStatement(raw.GetStmt())
	rebaseSource(s.query.GetAst().ProtoReflect(), source.Meta.GetSource())

	return nil
}

func rebaseSource(message protoreflect.Message, source *ir.SourceSpan) {
	walkMessages(message, func(message proto.Message) {
		switch value := message.(type) {
		case *ir.Metadata:
			value.Source = proto.CloneOf(source)
		default:
		}
	})
}

func walkMessages(message protoreflect.Message, visit func(proto.Message)) {
	visit(message.Interface())

	fields := message.Descriptor().Fields()
	for index := range fields.Len() {
		field := fields.Get(index)
		if field.Message() == nil || !message.Has(field) {
			continue
		}

		if field.IsList() {
			list := message.Get(field).List()
			for index := range list.Len() {
				walkMessages(list.Get(index).Message(), visit)
			}
		} else {
			walkMessages(message.Get(field).Message(), visit)
		}
	}
}

type rewrittenSQL struct {
	sql   string
	names map[int32]string
	mixed bool
}

func rewrite(parser parse.Parser, sql string, operators map[string][]*ir.BuiltinOperator) (rewrittenSQL, error) {
	scan, err := parser.Scan(sql)
	if err != nil {
		return rewrittenSQL{}, err
	}

	tokens := scan.GetTokens()
	names := make(map[int32]string)
	positions := make(map[string]int32)

	var output strings.Builder

	previous := 0
	nextNumber := int32(1)
	positional := false

	for index, token := range tokens {
		start, end := int(token.GetStart()), int(token.GetEnd())

		if token.GetToken() == pg.Token_PARAM {
			positional = true
		}

		text := sql[start:end]
		if !strings.HasSuffix(text, "@") || (text != "@" && len(operators[text]) > 0) || index+1 == len(tokens) {
			continue
		}

		start = end - 1

		next := tokens[index+1]
		if next.GetStart() != token.GetEnd() {
			continue
		}

		name := sql[int(next.GetStart()):int(next.GetEnd())]
		if !identifier(name) {
			continue
		}

		number := positions[name]
		if number == 0 {
			number = nextNumber
			nextNumber++
			positions[name] = number
			names[number] = name
		}

		output.WriteString(sql[previous:start])
		output.WriteByte('$')
		output.WriteString(strconv.Itoa(int(number)))
		previous = int(next.GetEnd())
	}

	output.WriteString(sql[previous:])

	return rewrittenSQL{sql: output.String(), names: names, mixed: positional && len(names) > 0}, nil
}

func identifier(name string) bool {
	for index, char := range name {
		if char != '_' && !unicode.IsLetter(char) && (index == 0 || !unicode.IsDigit(char)) {
			return false
		}
	}

	return name != ""
}

func (s *inference) parameterAnnotations(param *ir.QueryParameter) {
	for _, annotation := range s.query.GetMeta().GetAnnotations() {
		if annotation.GetNamespace() != "ratel" || len(annotation.GetArgs()) == 0 {
			continue
		}

		name := annotation.GetArgs()[0].GetValue()
		if name != param.GetName() && name != "$"+strconv.Itoa(int(param.GetNumber())) {
			continue
		}

		switch annotation.GetName() {
		case nullableAnnotation:
			param.Nullable = true
		case notnullAnnotation:
			param.Nullable = false
		default:
		}
	}
}

func (s *inference) annotations() {
	for _, annotation := range s.query.GetMeta().GetAnnotations() {
		if annotation.GetNamespace() != "ratel" ||
			!slices.Contains([]string{nullableAnnotation, notnullAnnotation}, annotation.GetName()) {
			continue
		}

		name := annotation.GetArgs()[0].GetValue()
		found := false

		for _, param := range s.query.GetParameters() {
			if param.GetName() == name || "$"+strconv.Itoa(int(param.GetNumber())) == name {
				param.Nullable = annotation.GetName() == nullableAnnotation
				found = true
			}
		}

		for _, col := range s.query.GetColumns() {
			if col.GetName() == name && annotation.GetName() == notnullAnnotation {
				col.Nullable = false
				found = true
			}
		}

		if !found {
			s.fail("annotation target " + name)
		}
	}
}
