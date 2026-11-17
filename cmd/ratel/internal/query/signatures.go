package query

import (
	"google.golang.org/protobuf/proto"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/overload"
)

func (s *inference) resolveType(typ *ir.TypeRef) *ir.TypeRef {
	if typ.GetElement() != nil {
		return overload.Array(s.resolveType(typ.GetElement()))
	}

	key := overload.TypeKey(typ)

	info := s.resolver.Lookup(key)
	if info == nil && typ.GetUdt() == nil {
		for _, namespace := range s.catalog.GetSearchPath() {
			if found := s.resolver.Lookup(overload.NameKey([]string{namespace, typ.GetPgName()})); found != nil {
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

func columnTypes(values []*ir.QueryColumn) []*ir.TypeRef {
	types := make([]*ir.TypeRef, len(values))
	for index, value := range values {
		types[index] = value.GetType()
	}

	return types
}

func (s *inference) common(values []*ir.QueryColumn) *ir.TypeRef {
	typ, problem := s.resolver.Common(columnTypes(values))
	if problem != "" {
		s.fail(problem)
	}

	return typ
}

func completeType(typ *ir.TypeRef) bool {
	if overload.IsUnknown(typ) || typ.GetKind() == ir.TypeKind_TYPE_KIND_UNSPECIFIED ||
		typ.GetKind() == ir.TypeKind_TYPE_KIND_PSEUDO {
		return false
	}

	if typ.GetElement() != nil {
		return completeType(typ.GetElement())
	}

	return true
}

// function resolves a call against the catalog's search path; a call no
// signature takes, or several take equally, rejects the query.
func (s *inference) function(
	name []string, args []*ir.QueryColumn, named []string, variadic bool,
) *overload.Candidate {
	chosen, err := s.resolver.Function(name, s.catalog.GetSearchPath(), columnTypes(args), named, variadic)
	if err != nil {
		s.reject(err.Error())

		return &overload.Candidate{}
	}

	return chosen
}

func (s *inference) operator(name string, args []*ir.QueryColumn) *overload.Candidate {
	chosen, err := s.resolver.Operator(name, columnTypes(args))
	if err != nil {
		s.reject(err.Error())

		return &overload.Candidate{}
	}

	return chosen
}
