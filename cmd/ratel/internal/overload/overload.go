// Package overload chooses the function or operator PostgreSQL would for a
// call, after its func_select_candidate and oper_select_candidate rules:
// exact matches first, then implicit casts scored by exact and preferred
// types, then unknown arguments resolved by type category (the string
// category first), then unknown arguments assumed to be the known one.
//
// Arguments are types; nil, or the pseudo-type unknown, is an argument
// whose type is not known yet (an untyped string literal or NULL). Query
// inference and the catalog builder share this resolver, so a schema
// expression and a query resolve the same call the same way.
package overload

import (
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/objectid"
)

const (
	// Unknown is the pseudo-type of an untyped literal.
	Unknown        = "unknown"
	qualifiedParts = 2
	binaryArity    = 2
	exactScore     = 100
	stringCategory = "S"
	pgCatalog      = "pg_catalog"
)

// Resolver holds the routines, operators, types and implicit casts calls
// resolve against.
type Resolver struct {
	functions map[string][]*ir.Function
	operators map[string][]*ir.BuiltinOperator
	types     map[string]*ir.BuiltinType
	casts     map[string]bool
}

// Candidate is a chosen signature: concrete input and result types (a
// polymorphic one instantiated) and, for a function, the routine.
type Candidate struct {
	Inputs   []*ir.TypeRef
	Result   *ir.TypeRef
	Function *ir.Function
	score    int
}

// Error is a call no signature takes (Ambiguous false) or several take
// equally well. Call spells the call with its argument types, as
// PostgreSQL does in its errors.
type Error struct {
	Ambiguous bool
	Call      string
}

func (e *Error) Error() string {
	if e.Ambiguous {
		return "ambiguous " + e.Call
	}

	return "no " + e.Call
}

// New collects the signatures of the builtin schemas and the catalog's.
func New(cat *ir.Catalog, builtins []*ir.Schema) *Resolver {
	result := &Resolver{
		functions: make(map[string][]*ir.Function), operators: make(map[string][]*ir.BuiltinOperator),
		types: make(map[string]*ir.BuiltinType), casts: make(map[string]bool),
	}

	for _, schema := range append(slices.Clone(builtins), cat.GetSchemas()...) {
		for _, function := range schema.GetFunctions() {
			parts := function.GetName().GetParts()
			if len(parts) < qualifiedParts {
				continue
			}

			key := NameKey(parts[:qualifiedParts])
			result.functions[key] = append(result.functions[key], function)
		}

		for _, operator := range schema.GetBuiltinOperators() {
			result.operators[operator.GetName()] = append(result.operators[operator.GetName()], operator)
		}

		for _, typ := range schema.GetBuiltinTypes() {
			result.types[TypeKey(typ.GetType())] = typ
			result.types[typ.GetType().GetPgName()] = typ
		}

		result.userTypes(schema)

		for _, cast := range schema.GetBuiltinCasts() {
			if cast.GetContext() == "i" {
				result.casts[TypeKey(cast.GetSource())+":"+TypeKey(cast.GetTarget())] = true
			}
		}
	}

	return result
}

func (r *Resolver) userTypes(schema *ir.Schema) {
	for _, table := range schema.GetTables() {
		r.addType(table.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_TABLE, nil)
	}

	for _, view := range schema.GetViews() {
		r.addType(view.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_VIEW, nil)
	}

	for _, view := range schema.GetMaterializedViews() {
		r.addType(view.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW, nil)
	}

	for _, value := range schema.GetEnums() {
		r.addType(value.GetName(), ir.TypeKind_TYPE_KIND_ENUM, ir.ObjectKind_OBJECT_KIND_ENUM_TYPE, nil)
	}

	for _, value := range schema.GetDomains() {
		r.addType(value.GetName(), ir.TypeKind_TYPE_KIND_DOMAIN, ir.ObjectKind_OBJECT_KIND_DOMAIN_TYPE, value.GetBaseType())
	}

	for _, value := range schema.GetComposites() {
		r.addType(value.GetName(), ir.TypeKind_TYPE_KIND_COMPOSITE, ir.ObjectKind_OBJECT_KIND_COMPOSITE_TYPE, nil)
	}

	for _, value := range schema.GetRanges() {
		r.addType(value.GetName(), ir.TypeKind_TYPE_KIND_RANGE, ir.ObjectKind_OBJECT_KIND_RANGE_TYPE, nil)
	}
}

func (r *Resolver) addType(name *ir.QualifiedName, kind ir.TypeKind, object ir.ObjectKind, base *ir.TypeRef) {
	parts := name.GetParts()
	typ := &ir.TypeRef{
		Kind: kind, PgName: parts[len(parts)-1], Udt: &ir.ObjectRef{Kind: object, Name: proto.CloneOf(name)},
	}
	r.types[TypeKey(typ)] = &ir.BuiltinType{Type: typ, Base: base}
}

// Lookup is the known type with key (TypeKey or a qualified name); nil when
// no type has it.
func (r *Resolver) Lookup(key string) *ir.BuiltinType {
	return r.types[key]
}

// Functions are the routines named by a qualified name (schema, name).
func (r *Resolver) Functions(name []string) []*ir.Function {
	return r.functions[NameKey(name)]
}

// IsOperator reports whether name is the name of some operator.
func (r *Resolver) IsOperator(name string) bool {
	return len(r.operators[name]) > 0
}

// Scalar is the scalar type named name.
func Scalar(name string) *ir.TypeRef {
	return &ir.TypeRef{Kind: ir.TypeKind_TYPE_KIND_SCALAR, PgName: name}
}

// Array is the one-dimensional array of element.
func Array(element *ir.TypeRef) *ir.TypeRef {
	return &ir.TypeRef{
		Kind: ir.TypeKind_TYPE_KIND_ARRAY, PgName: "_" + element.GetPgName(), Element: element, ArrayDimensions: 1,
	}
}

// NameKey is the lookup key of a qualified name.
func NameKey(parts []string) string {
	return objectid.ID(&ir.ObjectRef{Name: &ir.QualifiedName{Parts: parts}})
}

// TypeKey is the identity of a type: arrays by element, user types by
// object, builtins by name.
func TypeKey(typ *ir.TypeRef) string {
	if typ.GetElement() != nil {
		return TypeKey(typ.GetElement()) + "[]"
	}

	if typ.GetUdt() != nil {
		return objectid.ID(typ.GetUdt())
	}

	return typ.GetPgName()
}

// IsUnknown reports an argument whose type is not known yet.
func IsUnknown(typ *ir.TypeRef) bool { return typ == nil || typ.GetPgName() == Unknown }

// Coercible reports whether source converts to target implicitly (an
// unknown converts to anything).
func (r *Resolver) Coercible(source, target *ir.TypeRef) bool {
	if IsUnknown(source) || TypeKey(source) == TypeKey(target) {
		return true
	}

	source, target = r.BaseType(source), r.BaseType(target)
	if source == nil || target == nil {
		return false
	}

	if TypeKey(source) == TypeKey(target) {
		return true
	}

	if source.GetElement() != nil && target.GetElement() != nil {
		return r.Coercible(source.GetElement(), target.GetElement())
	}

	return r.casts[TypeKey(source)+":"+TypeKey(target)]
}

// BaseType unwraps domains to their base type; nil when one is unknown.
func (r *Resolver) BaseType(typ *ir.TypeRef) *ir.TypeRef {
	for range len(r.types) + 1 {
		if typ.GetKind() != ir.TypeKind_TYPE_KIND_DOMAIN {
			return typ
		}

		typ = r.types[TypeKey(typ)].GetBase()
	}

	return nil
}

func sameTypes(values []*ir.TypeRef) bool {
	if len(values) == 0 {
		return false
	}

	key := TypeKey(values[0])
	for _, value := range values {
		if TypeKey(value) != key {
			return false
		}
	}

	return true
}

// Common resolves the type values unify to, as UNION, CASE and
// anycompatible arguments do; problem says why they do not.
func (r *Resolver) Common(values []*ir.TypeRef) (*ir.TypeRef, string) {
	var result *ir.TypeRef

	preserveDomain := sameTypes(values)

	for _, value := range values {
		typ := value
		if IsUnknown(typ) {
			continue
		}

		if !preserveDomain {
			typ = r.BaseType(typ)
			if typ == nil {
				return Scalar("text"), "domain base " + TypeKey(value)
			}
		}

		if result == nil {
			result = typ

			continue
		}

		if TypeKey(result) == TypeKey(typ) {
			continue
		}

		if r.Coercible(result, typ) && !r.Coercible(typ, result) {
			result = typ

			continue
		}

		if !r.Coercible(typ, result) {
			return proto.CloneOf(result), "incompatible types " + TypeKey(result) + " and " + TypeKey(typ)
		}
	}

	if result == nil {
		return Scalar("text"), ""
	}

	return proto.CloneOf(result), ""
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

// namedInputs matches a call with named arguments to function as PostgreSQL
// does: positional arguments first, each named one to the input parameter of
// that name, every parameter without a default supplied once. Variadic
// functions take no named arguments here.
func namedInputs(function *ir.Function, named []string) []*ir.TypeRef {
	if function.GetVariadicElement() != nil {
		return nil
	}

	var params []*ir.Argument

	for _, arg := range function.GetArguments() {
		if arg.GetMode() != ir.ArgMode_ARG_MODE_OUT {
			params = append(params, arg)
		}
	}

	supplied := make([]bool, len(params))
	result := make([]*ir.TypeRef, len(named))

	for position, name := range named {
		index := position
		if name != "" {
			index = slices.IndexFunc(params, func(param *ir.Argument) bool { return param.GetName() == name })
		}

		if index < 0 || index >= len(params) || supplied[index] {
			return nil
		}

		supplied[index] = true
		result[position] = params[index].GetType()
	}

	for index := range len(params) - int(function.GetDefaultArguments()) {
		if !supplied[index] {
			return nil
		}
	}

	return result
}

func arrayParameter(typ *ir.TypeRef) bool {
	return typ.GetPgName() == "anyarray" || typ.GetPgName() == "anycompatiblearray"
}

func polymorphic(typ *ir.TypeRef) bool { return strings.HasPrefix(typ.GetPgName(), "any") }

func (r *Resolver) polymorphicElement(input, args []*ir.TypeRef) (*ir.TypeRef, bool) {
	var element *ir.TypeRef

	for index, typ := range input {
		actual := args[index]
		if typ.GetPgName() == "any" || !polymorphic(typ) || IsUnknown(actual) {
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

		if element != nil && TypeKey(element) != TypeKey(actual) {
			if !strings.HasPrefix(typ.GetPgName(), "anycompatible") {
				return nil, false
			}

			// A candidate whose arguments do not unify is not a match.
			unified, problem := r.Common([]*ir.TypeRef{element, actual})
			if problem != "" {
				return nil, false
			}

			element = unified
		} else {
			element = actual
		}
	}

	return element, true
}

func (r *Resolver) instantiate(input []*ir.TypeRef, result *ir.TypeRef, args []*ir.TypeRef) *Candidate {
	element, ok := r.polymorphicElement(input, args)
	if !ok {
		return nil
	}

	concrete := make([]*ir.TypeRef, len(input))
	for index, typ := range input {
		concrete[index] = substitute(typ, element)
		if typ.GetPgName() == "any" {
			concrete[index] = args[index]
			if IsUnknown(concrete[index]) {
				concrete[index] = Scalar("text")
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

	return &Candidate{Inputs: concrete, Result: output, score: score}
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
		return Array(element)
	}

	return element
}

func (r *Resolver) scoreCandidate(item *Candidate, args []*ir.TypeRef) bool {
	for index, typ := range item.Inputs {
		actual := args[index]
		if !r.Coercible(actual, typ) {
			return false
		}

		if !IsUnknown(actual) && TypeKey(r.BaseType(actual)) == TypeKey(typ) {
			item.score += exactScore

			continue
		}

		info := r.types[TypeKey(typ)]
		if !IsUnknown(actual) && info.GetPreferred() &&
			r.Category(r.BaseType(actual)) == r.Category(typ) {
			item.score++
		}
	}

	return true
}

func exactArguments(item *Candidate, args []*ir.TypeRef) bool {
	if item.score < 0 {
		return false
	}

	for index, input := range item.Inputs {
		if IsUnknown(args[index]) || TypeKey(args[index]) != TypeKey(input) {
			return false
		}
	}

	return true
}

// Category is the type's pg_type.typcategory.
func (r *Resolver) Category(typ *ir.TypeRef) string {
	if info := r.types[TypeKey(typ)]; info.GetCategory() != "" {
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
		if base := r.BaseType(typ); base != nil {
			return r.Category(base)
		}
	default:
	}

	return ""
}

func (r *Resolver) bestCandidates(items []*Candidate, args []*ir.TypeRef) []*Candidate {
	exact := make([]*Candidate, 0, 1)

	for _, item := range items {
		if exactArguments(item, args) {
			exact = append(exact, item)
		}
	}

	if len(exact) > 0 {
		return exact
	}

	best := make([]*Candidate, 0, len(items))

	for _, item := range items {
		if !r.scoreCandidate(item, args) {
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

// choose picks the one candidate PostgreSQL would; call names the function
// or operator with its argument types for the error.
func (r *Resolver) choose(items []*Candidate, args []*ir.TypeRef, call string) (*Candidate, error) {
	best := r.bestCandidates(items, args)
	if len(best) == 1 {
		return best[0], nil
	}

	ambiguous := len(best) > 1
	if ambiguous {
		filtered := r.unknownCandidates(best, args)
		if len(filtered) == 1 {
			return filtered[0], nil
		}

		if len(filtered) > 0 {
			best = filtered
		}

		best = r.assumedCandidates(best, args)
		if len(best) == 1 {
			return best[0], nil
		}
	}

	return nil, &Error{Ambiguous: ambiguous, Call: call}
}

func (r *Resolver) unknownCandidates(items []*Candidate, args []*ir.TypeRef) []*Candidate {
	categories := make([]string, len(args))
	preferred := make([]bool, len(args))

	for index, arg := range args {
		if !IsUnknown(arg) {
			continue
		}

		category := r.Category(items[0].Inputs[index])
		conflict := false

		for _, item := range items {
			candidateCategory := r.Category(item.Inputs[index])
			if candidateCategory == stringCategory {
				category = stringCategory
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
			if r.Category(item.Inputs[index]) == category && r.types[TypeKey(item.Inputs[index])].GetPreferred() {
				preferred[index] = true
			}
		}
	}

	result := make([]*Candidate, 0, len(items))

	for _, item := range items {
		if r.matchesUnknown(item, args, categories, preferred) {
			result = append(result, item)
		}
	}

	return result
}

func (r *Resolver) matchesUnknown(item *Candidate, args []*ir.TypeRef, categories []string, preferred []bool) bool {
	for index, arg := range args {
		if !IsUnknown(arg) {
			continue
		}

		typ := item.Inputs[index]
		if r.Category(typ) != categories[index] ||
			(preferred[index] && !r.types[TypeKey(typ)].GetPreferred()) {
			return false
		}
	}

	return true
}

func (r *Resolver) assumedCandidates(items []*Candidate, args []*ir.TypeRef) []*Candidate {
	var known *ir.TypeRef

	for _, arg := range args {
		if IsUnknown(arg) {
			continue
		}

		typ := r.BaseType(arg)
		if known != nil && TypeKey(known) != TypeKey(typ) {
			return nil
		}

		known = typ
	}

	if known == nil {
		return nil
	}

	result := make([]*Candidate, 0, len(items))

	for _, item := range items {
		matches := true

		for index, arg := range args {
			if IsUnknown(arg) && !r.Coercible(known, item.Inputs[index]) {
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

// Function resolves a call of name. An unqualified name looks in
// pg_catalog, then in each of namespaces; named holds each argument's name
// ("" for a positional one) and variadic marks a VARIADIC array argument.
func (r *Resolver) Function(
	name []string, namespaces []string, args []*ir.TypeRef, named []string, variadic bool,
) (*Candidate, error) {
	keys := []string{NameKey(name)}
	if len(name) == 1 {
		keys = make([]string, 0, 1+len(namespaces))
		keys = append(keys, NameKey([]string{pgCatalog, name[0]}))

		for _, schema := range namespaces {
			keys = append(keys, NameKey([]string{schema, name[0]}))
		}
	}

	var candidates []*Candidate

	seen := make(map[string]bool)

	for _, key := range keys {
		for _, function := range r.functions[key] {
			inputs := functionInputs(function, len(args), variadic)
			if slices.ContainsFunc(named, func(name string) bool { return name != "" }) {
				inputs = namedInputs(function, named)
			}

			if inputs == nil && len(args) != 0 {
				continue
			}

			if inputs == nil && len(function.GetArguments()) != 0 {
				continue
			}

			// The first namespace with a signature hides the same signature
			// later on the path, pg_catalog first, as in PostgreSQL.
			parts := function.GetName().GetParts()

			identity := parts[len(parts)-1]
			if seen[identity] {
				continue
			}

			seen[identity] = true

			returns := FunctionResult(function)
			if returns == nil {
				continue
			}

			item := r.instantiate(inputs, returns, args)
			if item != nil {
				item.Function = function
				candidates = append(candidates, item)
			}
		}
	}

	return r.choose(candidates, args, "function "+strings.Join(name, ".")+"("+strings.Join(TypeNames(args), ", ")+")")
}

// FunctionResult is the type a function's call yields; nil for void and
// trigger functions.
func FunctionResult(function *ir.Function) *ir.TypeRef {
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

// Operator resolves the operator name over args: one argument for a prefix
// operator, two for an infix one.
func (r *Resolver) Operator(name string, args []*ir.TypeRef) (*Candidate, error) {
	candidates := make([]*Candidate, 0, len(r.operators[name]))

	for _, operator := range r.operators[name] {
		input := []*ir.TypeRef{operator.GetRight()}
		if operator.GetLeft() != nil {
			input = []*ir.TypeRef{operator.GetLeft(), operator.GetRight()}
		}

		if len(input) != len(args) {
			continue
		}

		item := r.instantiate(input, operator.GetResult(), args)
		if item != nil {
			candidates = append(candidates, item)
		}
	}
	// PostgreSQL resolves an unknown binary operand as the known operand first.
	if len(args) == binaryArity && IsUnknown(args[0]) != IsUnknown(args[1]) {
		known := args[0]
		if IsUnknown(known) {
			known = args[1]
		}

		for _, item := range candidates {
			if TypeKey(item.Inputs[0]) == TypeKey(known) && TypeKey(item.Inputs[1]) == TypeKey(known) {
				return item, nil
			}
		}
	}

	operands := TypeNames(args)
	if len(operands) == 1 {
		return r.choose(candidates, args, "operator "+name+" "+operands[0])
	}

	return r.choose(candidates, args, "operator "+strings.Join(operands, " "+name+" "))
}

// TypeNames spells argument types as PostgreSQL does in its errors.
func TypeNames(args []*ir.TypeRef) []string {
	names := make([]string, len(args))
	for index, arg := range args {
		names[index] = TypeName(arg)
	}

	return names
}

// TypeName spells one type as PostgreSQL does in its errors.
func TypeName(typ *ir.TypeRef) string {
	switch {
	case IsUnknown(typ):
		return Unknown
	case typ.GetKind() == ir.TypeKind_TYPE_KIND_ARRAY:
		return TypeName(typ.GetElement()) + "[]"
	default:
		return typ.GetPgName()
	}
}
