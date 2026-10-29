package query

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
)

const (
	unknownType    = "unknown"
	binaryArity    = 2
	qualifiedParts = 2
)

type signatures struct {
	functions map[string][]*ir.Function
	operators map[string][]*ir.BuiltinOperator
	types     map[string]*ir.BuiltinType
	casts     map[string]bool
}

type candidate struct {
	inputs   []*ir.TypeRef
	result   *ir.TypeRef
	function *ir.Function
	score    int
}

func newSignatures(cat *ir.Catalog, builtins []*ir.Schema) signatures {
	result := signatures{
		functions: make(map[string][]*ir.Function), operators: make(map[string][]*ir.BuiltinOperator),
		types: make(map[string]*ir.BuiltinType), casts: make(map[string]bool),
	}

	for _, schema := range append(slices.Clone(builtins), cat.GetSchemas()...) {
		for _, function := range schema.GetFunctions() {
			parts := function.GetName().GetParts()
			if len(parts) < qualifiedParts {
				continue
			}

			key := nameKey(parts[:qualifiedParts])
			result.functions[key] = append(result.functions[key], function)
		}

		for _, operator := range schema.GetBuiltinOperators() {
			result.operators[operator.GetName()] = append(result.operators[operator.GetName()], operator)
		}

		for _, typ := range schema.GetBuiltinTypes() {
			result.types[typeKey(typ.GetType())] = typ
			result.types[typ.GetType().GetPgName()] = typ
		}

		result.userTypes(schema)

		for _, cast := range schema.GetBuiltinCasts() {
			if cast.GetContext() == "i" {
				result.casts[typeKey(cast.GetSource())+":"+typeKey(cast.GetTarget())] = true
			}
		}
	}

	return result
}

func (s signatures) userTypes(schema *ir.Schema) {
	for _, table := range schema.GetTables() {
		s.addType(table.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_TABLE, nil)
	}

	for _, view := range schema.GetViews() {
		s.addType(view.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_VIEW, nil)
	}

	for _, view := range schema.GetMaterializedViews() {
		s.addType(view.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW, nil)
	}

	for _, value := range schema.GetEnums() {
		s.addType(value.GetName(), ir.TypeKind_TYPE_KIND_ENUM, ir.ObjectKind_OBJECT_KIND_ENUM_TYPE, nil)
	}

	for _, value := range schema.GetDomains() {
		s.addType(value.GetName(), ir.TypeKind_TYPE_KIND_DOMAIN, ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE, value.GetBaseType())
	}

	for _, value := range schema.GetComposites() {
		s.addType(value.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, nil)
	}

	for _, value := range schema.GetRanges() {
		s.addType(value.GetName(), ir.TypeKind_TYPE_KIND_RANGE, ir.ObjectKind_OBJECT_KIND_RANGE_TYPE, nil)
	}
}

func (s signatures) addType(name *ir.QualifiedName, kind ir.TypeKind, object ir.ObjectKind, base *ir.TypeRef) {
	parts := name.GetParts()
	typ := &ir.TypeRef{
		Kind: kind, PgName: parts[len(parts)-1], Udt: &ir.ObjectRef{Kind: object, Name: proto.CloneOf(name)},
	}
	s.types[typeKey(typ)] = &ir.BuiltinType{Type: typ, Base: base}
}

func (s *inference) resolveType(typ *ir.TypeRef) *ir.TypeRef {
	if typ.GetElement() != nil {
		return array(s.resolveType(typ.GetElement()))
	}

	key := typeKey(typ)

	info := s.signatures.types[key]
	if info == nil && typ.GetUdt() == nil {
		for _, namespace := range s.catalog.GetSearchPath() {
			if found := s.signatures.types[nameKey([]string{namespace, typ.GetPgName()})]; found != nil {
				info = found

				break
			}
		}
	}

	if info == nil {
		s.fail("type " + key)

		return nil
	}

	result := proto.CloneOf(info.GetType())
	result.Modifier = proto.CloneOf(typ.GetModifier())

	return result
}

func scalar(name string) *ir.TypeRef {
	return &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: name}
}

func array(element *ir.TypeRef) *ir.TypeRef {
	return &ir.TypeRef{
		Kind: ir.TypeKind_TYPE_KIND_ARRAY, PgName: "_" + element.GetPgName(), Element: element, ArrayDimensions: 1,
	}
}

func nameKey(parts []string) string {
	return catalog.ObjectID(&ir.ObjectRef{Name: &ir.QualifiedName{Parts: parts}})
}

func typeKey(typ *ir.TypeRef) string {
	if typ.GetElement() != nil {
		return typeKey(typ.GetElement()) + "[]"
	}

	if typ.GetUdt() != nil {
		return catalog.ObjectID(typ.GetUdt())
	}

	return typ.GetPgName()
}
func unknown(typ *ir.TypeRef) bool { return typ == nil || typ.GetPgName() == unknownType }

func (s signatures) coercible(source, target *ir.TypeRef) bool {
	if unknown(source) || typeKey(source) == typeKey(target) {
		return true
	}

	source, target = s.baseType(source), s.baseType(target)
	if source == nil || target == nil {
		return false
	}

	if typeKey(source) == typeKey(target) {
		return true
	}

	if source.GetElement() != nil && target.GetElement() != nil {
		return s.coercible(source.GetElement(), target.GetElement())
	}

	return s.casts[typeKey(source)+":"+typeKey(target)]
}

func (s signatures) baseType(typ *ir.TypeRef) *ir.TypeRef {
	for range len(s.types) + 1 {
		if typ.GetKind() != ir.TypeKind_TYPE_KIND_DOMAIN {
			return typ
		}

		typ = s.types[typeKey(typ)].GetBase()
	}

	return nil
}

func sameTypes(values []*ir.QueryColumn) bool {
	if len(values) == 0 {
		return false
	}

	key := typeKey(values[0].GetType())
	for _, value := range values {
		if typeKey(value.GetType()) != key {
			return false
		}
	}

	return true
}

func (s *inference) common(values []*ir.QueryColumn) *ir.TypeRef {
	var result *ir.TypeRef

	preserveDomain := sameTypes(values)

	for _, value := range values {
		typ := value.GetType()
		if unknown(typ) {
			continue
		}

		if !preserveDomain {
			typ = s.signatures.baseType(typ)
			if typ == nil {
				s.fail("domain base " + typeKey(value.GetType()))

				continue
			}
		}

		if result == nil {
			result = typ

			continue
		}

		if typeKey(result) == typeKey(typ) {
			continue
		}

		if s.signatures.coercible(result, typ) && !s.signatures.coercible(typ, result) {
			result = typ

			continue
		}

		if !s.signatures.coercible(typ, result) {
			s.fail("incompatible types " + typeKey(result) + " and " + typeKey(typ))
		}
	}

	if result == nil {
		return scalar("text")
	}

	return proto.CloneOf(result)
}

func functionInputs(function *ir.Function, count int, variadic bool) []*ir.TypeRef {
	result := make([]*ir.TypeRef, 0, len(function.GetArguments()))

	for _, arg := range function.GetArguments() {
		if arg.GetMode() != ir.ArgMode_ARG_MODE_OUT {
			result = append(result, arg.GetType())
		}
	}

	minimum := len(result) - int(function.GetDefaultArguments())
	if function.GetVariadicElement() != nil && !variadic {
		minimum--
		if count < minimum {
			return nil
		}

		result = result[:len(result)-1]
		for len(result) < count {
			result = append(result, function.GetVariadicElement())
		}
	}

	if count < minimum || count > len(result) {
		return nil
	}

	return result[:count]
}

func arrayParameter(typ *ir.TypeRef) bool {
	return typ.GetPgName() == "anyarray" || typ.GetPgName() == "anycompatiblearray"
}

func completeType(typ *ir.TypeRef) bool {
	if unknown(typ) || typ.GetKind() == ir.TypeKind_TYPE_KIND_UNSPECIFIED ||
		typ.GetKind() == ir.TypeKind_TYPE_KIND_PSEUDO {
		return false
	}

	if typ.GetElement() != nil {
		return completeType(typ.GetElement())
	}

	return true
}

func polymorphic(typ *ir.TypeRef) bool { return strings.HasPrefix(typ.GetPgName(), "any") }

func (s *inference) polymorphicElement(input []*ir.TypeRef, args []*ir.QueryColumn) (*ir.TypeRef, bool) {
	var element *ir.TypeRef

	for index, typ := range input {
		actual := args[index].GetType()
		if typ.GetPgName() == "any" || !polymorphic(typ) || unknown(actual) {
			continue
		}

		if !acceptsPolymorphic(typ, actual) {
			return nil, false
		}

		if arrayParameter(typ) {
			actual = actual.GetElement()
		}

		if actual == nil {
			return nil, false
		}

		if element != nil && typeKey(element) != typeKey(actual) {
			if !strings.HasPrefix(typ.GetPgName(), "anycompatible") {
				return nil, false
			}

			element = s.common([]*ir.QueryColumn{{Type: element}, {Type: actual}})
		} else {
			element = actual
		}
	}

	return element, true
}

func (s *inference) instantiate(input []*ir.TypeRef, result *ir.TypeRef, args []*ir.QueryColumn) *candidate {
	element, ok := s.polymorphicElement(input, args)
	if !ok {
		return nil
	}

	concrete := make([]*ir.TypeRef, len(input))
	for index, typ := range input {
		concrete[index] = substitute(typ, element)
		if typ.GetPgName() == "any" {
			concrete[index] = args[index].GetType()
			if concrete[index].GetPgName() == unknownType {
				concrete[index] = scalar("text")
			}
		}

		if concrete[index] == nil {
			return nil
		}
	}

	output := substitute(result, element)
	if output == nil {
		return nil
	}

	score := 0

	for _, typ := range input {
		if polymorphic(typ) {
			score--
		}
	}

	return &candidate{inputs: concrete, result: output, score: score}
}

func acceptsPolymorphic(typ, actual *ir.TypeRef) bool {
	switch typ.GetPgName() {
	case "anyarray", "anycompatiblearray":
		return actual.GetElement() != nil
	case "anyenum":
		return actual.GetKind() == ir.TypeKind_TYPE_KIND_ENUM
	case "anyrange", "anycompatiblerange", "anymultirange", "anycompatiblemultirange":
		return actual.GetKind() == ir.TypeKind_TYPE_KIND_RANGE
	case "anynonarray", "anycompatiblenonarray":
		return actual.GetElement() == nil
	default:
		return true
	}
}

func substitute(typ, element *ir.TypeRef) *ir.TypeRef {
	if !polymorphic(typ) {
		return typ
	}

	if element == nil {
		return nil
	}

	if arrayParameter(typ) {
		return array(element)
	}

	return element
}

func (s *inference) scoreCandidate(item *candidate, args []*ir.QueryColumn) bool {
	for index, typ := range item.inputs {
		actual := args[index].GetType()
		if !s.signatures.coercible(actual, typ) {
			return false
		}

		if !unknown(actual) && typeKey(s.signatures.baseType(actual)) == typeKey(typ) {
			item.score += 100

			continue
		}

		info := s.signatures.types[typeKey(typ)]
		if !unknown(actual) && info.GetPreferred() &&
			s.signatures.category(s.signatures.baseType(actual)) == s.signatures.category(typ) {
			item.score++
		}
	}

	return true
}

func exactArguments(item *candidate, args []*ir.QueryColumn) bool {
	if item.score < 0 {
		return false
	}

	for index, input := range item.inputs {
		if unknown(args[index].GetType()) || typeKey(args[index].GetType()) != typeKey(input) {
			return false
		}
	}

	return true
}

func (s signatures) category(typ *ir.TypeRef) string {
	if info := s.types[typeKey(typ)]; info.GetCategory() != "" {
		return info.GetCategory()
	}

	switch typ.GetKind() {
	case ir.TypeKind_TYPE_KIND_ARRAY:
		return "A"
	case ir.TypeKind_TYPE_KIND_ENUM:
		return "E"
	case ir.TypeKind_TYPE_KIND_COMPOSITE:
		return "C"
	case ir.TypeKind_TYPE_KIND_RANGE:
		return "R"
	case ir.TypeKind_TYPE_KIND_DOMAIN:
		if base := s.baseType(typ); base != nil {
			return s.category(base)
		}
	default:
	}

	return ""
}

func (s *inference) bestCandidates(items []*candidate, args []*ir.QueryColumn) []*candidate {
	exact := make([]*candidate, 0, 1)

	for _, item := range items {
		if exactArguments(item, args) {
			exact = append(exact, item)
		}
	}

	if len(exact) > 0 {
		return exact
	}

	best := make([]*candidate, 0, len(items))

	for _, item := range items {
		if !s.scoreCandidate(item, args) {
			continue
		}

		if len(best) == 0 || item.score > best[0].score {
			best = best[:0]
		}

		if len(best) == 0 || item.score == best[0].score {
			best = append(best, item)
		}
	}

	return best
}

func (s *inference) choose(items []*candidate, args []*ir.QueryColumn, name string) *candidate {
	best := s.bestCandidates(items, args)
	if len(best) == 1 {
		return best[0]
	}

	if len(best) > 1 {
		filtered := s.unknownCandidates(best, args)
		if len(filtered) == 1 {
			return filtered[0]
		}

		if len(filtered) > 0 {
			best = filtered
		}

		best = s.assumedCandidates(best, args)
		if len(best) == 1 {
			return best[0]
		}
	}

	s.fail("overload " + name)

	return &candidate{}
}

func (s *inference) unknownCandidates(items []*candidate, args []*ir.QueryColumn) []*candidate {
	categories := make([]string, len(args))
	preferred := make([]bool, len(args))

	for index, arg := range args {
		if !unknown(arg.GetType()) {
			continue
		}

		category := s.signatures.category(items[0].inputs[index])
		conflict := false

		for _, item := range items {
			candidateCategory := s.signatures.category(item.inputs[index])
			if candidateCategory == "S" {
				category = "S"
				conflict = false

				break
			}

			conflict = conflict || candidateCategory != category
		}

		if conflict {
			return nil
		}

		categories[index] = category

		for _, item := range items {
			if s.signatures.category(item.inputs[index]) == category &&
				s.signatures.types[typeKey(item.inputs[index])].GetPreferred() {
				preferred[index] = true
			}
		}
	}

	result := make([]*candidate, 0, len(items))

	for _, item := range items {
		if s.matchesUnknown(item, args, categories, preferred) {
			result = append(result, item)
		}
	}

	return result
}

func (s *inference) matchesUnknown(
	item *candidate, args []*ir.QueryColumn, categories []string, preferred []bool,
) bool {
	for index, arg := range args {
		if !unknown(arg.GetType()) {
			continue
		}

		typ := item.inputs[index]
		if s.signatures.category(typ) != categories[index] ||
			(preferred[index] && !s.signatures.types[typeKey(typ)].GetPreferred()) {
			return false
		}
	}

	return true
}

func (s *inference) assumedCandidates(items []*candidate, args []*ir.QueryColumn) []*candidate {
	var known *ir.TypeRef

	for _, arg := range args {
		if unknown(arg.GetType()) {
			continue
		}

		typ := s.signatures.baseType(arg.GetType())
		if known != nil && typeKey(known) != typeKey(typ) {
			return nil
		}

		known = typ
	}

	if known == nil {
		return nil
	}

	result := make([]*candidate, 0, len(items))

	for _, item := range items {
		matches := true

		for index, arg := range args {
			if unknown(arg.GetType()) && !s.signatures.coercible(known, item.inputs[index]) {
				matches = false

				break
			}
		}

		if matches {
			result = append(result, item)
		}
	}

	return result
}

func (s *inference) function(name []string, args []*ir.QueryColumn, variadic bool) *candidate {
	keys := []string{nameKey(name)}
	if len(name) == 1 {
		keys = make([]string, 0, 1+len(s.catalog.GetSearchPath()))
		keys = append(keys, nameKey([]string{"pg_catalog", name[0]}))

		for _, schema := range s.catalog.GetSearchPath() {
			keys = append(keys, nameKey([]string{schema, name[0]}))
		}
	}

	var candidates []*candidate

	seen := make(map[string]bool)

	for _, key := range keys {
		for _, function := range s.signatures.functions[key] {
			inputs := functionInputs(function, len(args), variadic)
			if inputs == nil && len(args) != 0 {
				continue
			}

			if inputs == nil && len(function.GetArguments()) != 0 {
				continue
			}

			parts := function.GetName().GetParts()

			identity := parts[len(parts)-1]
			if seen[identity] {
				continue
			}

			seen[identity] = true

			returns := functionResult(function)

			if returns == nil {
				continue
			}

			item := s.instantiate(inputs, returns, args)
			if item != nil {
				item.function = function
				candidates = append(candidates, item)
			}
		}
	}

	return s.choose(candidates, args, strings.Join(name, "."))
}

func functionResult(function *ir.Function) *ir.TypeRef {
	if typ := function.GetReturns().GetScalar(); typ != nil {
		return typ
	}

	if typ := function.GetReturns().GetSetof().GetType(); typ != nil {
		return typ
	}

	if function.GetReturns().GetTable() != nil {
		return &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_PSEUDO, PgName: "record"}
	}

	return nil
}

func (s *inference) operator(name string, args []*ir.QueryColumn) *candidate {
	candidates := make([]*candidate, 0, len(s.signatures.operators[name]))

	for _, operator := range s.signatures.operators[name] {
		input := []*ir.TypeRef{operator.GetRight()}
		if operator.GetLeft() != nil {
			input = []*ir.TypeRef{operator.GetLeft(), operator.GetRight()}
		}

		if len(input) != len(args) {
			continue
		}

		item := s.instantiate(input, operator.GetResult(), args)
		if item != nil {
			candidates = append(candidates, item)
		}
	}
	// PostgreSQL resolves an unknown binary operand as the known operand first.
	if len(args) == 2 && unknown(args[0].GetType()) != unknown(args[1].GetType()) {
		known := args[0].GetType()
		if unknown(known) {
			known = args[1].GetType()
		}

		for _, item := range candidates {
			if typeKey(item.inputs[0]) == typeKey(known) && typeKey(item.inputs[1]) == typeKey(known) {
				return item
			}
		}
	}

	return s.choose(candidates, args, name)
}
