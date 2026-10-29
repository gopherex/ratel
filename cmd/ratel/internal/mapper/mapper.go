// Package mapper adapts libpg_query syntax to ratel IR without catalog resolution.
package mapper

import (
	"strconv"
	"strings"
	"unicode/utf8"

	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// Mapper is the immutable source context of one top-level statement.
// Construct one per RawStmt; nested mappings retain its source provenance.
type Mapper struct {
	parser parse.Parser
	file   string
	sql    string
	start  int
	end    int
	path   string
}

// New binds a mapper to the original file text and a zero-based statement ordinal.
// RawStmt positions are byte offsets; a zero length means through end of input.
func New(parser parse.Parser, file, sql string, raw *pg.RawStmt, ordinal int) Mapper {
	start := min(max(int(raw.GetStmtLocation()), 0), len(sql))
	end := len(sql)

	if raw.GetStmtLen() > 0 {
		end = min(start+int(raw.GetStmtLen()), end)
	}

	return Mapper{
		parser: parser, file: file, sql: sql, start: start, end: end,
		path: strconv.Quote(file) + "/statement/" + strconv.Itoa(ordinal),
	}
}

// SourceSQL returns the original statement slice, excluding its terminating semicolon.
func (m Mapper) SourceSQL() string { return m.sql[m.start:m.end] }

func (m Mapper) metadata() *ir.Metadata { return &ir.Metadata{Source: m.span(m.start, m.end)} }

func (m Mapper) span(start, end int) *ir.SourceSpan {
	start = min(max(start, m.start), m.end)
	end = min(max(end, start), m.end)
	first := position(m.sql, start)
	last := position(m.sql, end)

	return &ir.SourceSpan{
		File: m.file, StartLine: first.line, StartColumn: first.column,
		EndLine: last.line, EndColumn: last.column,
		StartOffset: unsignedOffset(start), EndOffset: unsignedOffset(end),
	}
}

type coordinate struct {
	line   uint32
	column uint32
}

func position(sql string, offset int) coordinate {
	prefix := sql[:offset]
	line := boundedUint(strings.Count(prefix, "\n") + 1)
	last := strings.LastIndexByte(prefix, '\n') + 1

	return coordinate{line: line, column: boundedUint(utf8.RuneCountInString(prefix[last:]) + 1)}
}

func unsignedOffset(value int) uint64 {
	if value < 0 {
		return 0
	}

	return uint64(value)
}

// finish uses protobuf field paths, never pointer addresses or map iteration order.
// All messages with a metadata field receive the enclosing statement span unless
// a more precise AST location was supplied by their mapper.
func (m Mapper) finish(message proto.Message) {
	m.decorate(message.ProtoReflect(), m.path)
}

func (m Mapper) decorate(message protoreflect.Message, path string) {
	fields := message.Descriptor().Fields()
	if field := fields.ByName("node_id"); field != nil {
		message.Set(field, protoreflect.ValueOfString(path))
	}

	if field := fields.ByName("meta"); field != nil && !message.Has(field) {
		message.Set(field, protoreflect.ValueOfMessage(m.metadata().ProtoReflect()))
	}

	for index := range fields.Len() {
		field := fields.Get(index)
		if field.Message() == nil || !message.Has(field) || field.Name() == "meta" {
			continue
		}

		child := path + "/" + string(field.Name())

		if field.IsList() {
			list := message.Get(field).List()
			for item := range list.Len() {
				m.decorate(list.Get(item).Message(), child+"/"+strconv.Itoa(item))
			}
		} else {
			m.decorate(message.Get(field).Message(), child)
		}
	}
}

func stringsOf(nodes []*pg.Node) []string {
	result := make([]string, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, node.GetString_().GetSval())
	}

	return result
}

func qualified(nodes []*pg.Node) *ir.QualifiedName { return &ir.QualifiedName{Parts: stringsOf(nodes)} }

func relationName(relation *pg.RangeVar) *ir.QualifiedName {
	const maxNameParts = 3

	parts := make([]string, 0, maxNameParts)
	if relation.GetCatalogname() != "" {
		parts = append(parts, relation.GetCatalogname())
	}

	if relation.GetSchemaname() != "" {
		parts = append(parts, relation.GetSchemaname())
	}

	if relation.GetRelname() != "" {
		parts = append(parts, relation.GetRelname())
	}

	return &ir.QualifiedName{Parts: parts}
}

func tableObject(relation *pg.RangeVar) *ir.ObjectRef {
	return &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: relationName(relation)}
}
