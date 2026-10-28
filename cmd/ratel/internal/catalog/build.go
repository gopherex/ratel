// Package catalog assembles declarative PostgreSQL files into a resolved catalog.
package catalog

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
	pg "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

const (
	defaultSchema = "public"
	pgCatalog     = "pg_catalog"
)

// File is one complete schema source, with its diagnostic filename.
type File struct {
	Name string
	SQL  string
}

// Error is a declarative-input refusal. Code is stable; Source identifies its cause.
type Error struct {
	Code    string
	Message string
	Source  *ir.SourceSpan
}

func (e *Error) Error() string { return position(e.Source) + ": " + e.Code + ": " + e.Message }

type (
	declaration struct {
		reference *ir.ObjectRef
		message   proto.Message
		meta      *ir.Metadata
		scope     string
	}
	statement struct {
		syntax *pg.Node
		mapper mapper.Mapper
		schema string
	}
	attachment struct {
		table   *ir.QualifiedName
		message proto.Message
	}
	comment struct {
		reference *ir.ObjectRef
		meta      *ir.Metadata
	}
	builder struct {
		catalog     *ir.Catalog
		schemas     map[string]*ir.Schema
		objects     map[string]declaration
		namespaces  map[string]declaration
		statements  []statement
		attachments []attachment
		comments    []comment
		builtins    []*ir.Schema
		types       *pgtype.Map
	}
)

// Build collects declarations before resolving any references. Builtins are a
// pg_catalog snapshot for the target major, not part of the desired catalog.
// Call parse.Warm before invoking Build concurrently for the first time.
func Build(files []File, major int, builtins []*ir.Schema) (*ir.Catalog, error) {
	const minMajor, maxMajor = 16, 18
	if major < minMajor || major > maxMajor {
		return nil, &Error{Code: "RTL-C01", Message: "unsupported PostgreSQL major " + strconv.Itoa(major)}
	}

	state := newBuilder(major, builtins)
	ordered := slices.Clone(files)
	slices.SortFunc(ordered, func(left, right File) int { return cmp.Compare(left.Name, right.Name) })

	for _, file := range ordered {
		parsed, err := parse.Parse(file.SQL)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Name, err)
		}

		for ordinal, raw := range parsed.GetStmts() {
			mapped := mapper.New(file.Name, file.SQL, raw, ordinal)
			if err := state.collect(statement{syntax: raw.GetStmt(), mapper: mapped, schema: defaultSchema}); err != nil {
				return nil, err
			}
		}
	}

	for _, stmt := range state.statements {
		if stmt.syntax.GetCreateFunctionStmt() == nil {
			if err := state.dispatch(stmt); err != nil {
				return nil, err
			}
		}
	}

	for _, stmt := range state.statements {
		if stmt.syntax.GetCreateFunctionStmt() != nil {
			if err := state.dispatch(stmt); err != nil {
				return nil, err
			}
		}
	}

	if err := state.finish(); err != nil {
		return nil, err
	}

	return state.catalog, nil
}

func newBuilder(major int, builtins []*ir.Schema) *builder {
	public := &ir.Schema{Name: defaultSchema, Meta: &ir.Metadata{}}
	reference := &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_SCHEMA, Name: &ir.QualifiedName{Parts: []string{defaultSchema}},
	}
	objects := map[string]declaration{objectKey(reference): {
		reference: reference, message: public, meta: public.GetMeta(), scope: defaultSchema,
	}}

	return &builder{
		catalog: &ir.Catalog{
			DefaultSchema: defaultSchema, SearchPath: []string{defaultSchema}, DatabaseVersion: strconv.Itoa(major),
		},
		schemas: map[string]*ir.Schema{defaultSchema: public}, objects: objects,
		namespaces: make(map[string]declaration), builtins: builtins, types: pgtype.NewMap(),
	}
}

func (b *builder) collect(stmt statement) error {
	schema := stmt.syntax.GetCreateSchemaStmt()
	if schema == nil {
		b.statements = append(b.statements, stmt)

		return nil
	}

	mapped := stmt.mapper.MapCreateSchema(schema)
	reference := &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_SCHEMA, Name: &ir.QualifiedName{Parts: []string{mapped.GetName()}},
	}

	if err := b.register(reference, mapped, mapped.GetMeta(), "schema"); err != nil {
		return err
	}

	b.schemas[mapped.GetName()] = mapped
	for _, node := range schema.GetSchemaElts() {
		if err := b.collect(statement{syntax: node, mapper: stmt.mapper, schema: mapped.GetName()}); err != nil {
			return err
		}
	}

	return nil
}

func (b *builder) register(ref *ir.ObjectRef, message proto.Message, meta *ir.Metadata, namespace string) error {
	key := namespace + "/" + ObjectID(ref)
	if previous, exists := b.namespaces[key]; exists {
		return refusal("RTL-C02", meta, "duplicate "+ObjectID(ref)+"; first declared at "+position(previous.meta.GetSource()))
	}

	entry := declaration{reference: ref, message: message, meta: meta, scope: defaultSchema}
	b.namespaces[key] = entry
	b.objects[objectKey(ref)] = entry

	return nil
}

func (b *builder) add(message proto.Message, collection, scope string) error {
	value := message.ProtoReflect()
	nameField := value.Descriptor().Fields().ByName("name")
	name := value.Get(nameField).Message().Interface()

	var qualified *ir.QualifiedName

	switch typed := name.(type) {
	case *ir.QualifiedName:
		qualified = typed
	default:
		return refusal("RTL-C03", metadata(message), "invalid declaration name")
	}

	kind := collectionKind(collection)
	qualify(qualified, scope, kind)

	schema := b.schemas[qualified.GetParts()[0]]
	if schema == nil {
		return refusal("RTL-C03", metadata(message), "undeclared schema "+qualified.GetParts()[0])
	}

	ref := &ir.ObjectRef{Kind: kind, Name: qualified}
	if err := b.register(ref, message, metadata(message), namespace(kind)); err != nil {
		return err
	}

	entry := b.objects[objectKey(ref)]
	entry.scope = scope
	b.objects[objectKey(ref)] = entry

	if kind == ir.ObjectKind_OBJECT_KIND_TABLE || kind == ir.ObjectKind_OBJECT_KIND_VIEW ||
		kind == ir.ObjectKind_OBJECT_KIND_MATERIALIZED_VIEW {
		if previous, exists := b.namespaces["type/"+ObjectID(ref)]; exists {
			return refusal("RTL-C02", entry.meta, "duplicate row type; first declared at "+position(previous.meta.GetSource()))
		}

		b.namespaces["type/"+ObjectID(ref)] = entry
	}

	field := schema.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(collection))
	schema.ProtoReflect().Mutable(field).List().Append(protoreflect.ValueOfMessage(value))

	return nil
}

func metadata(message proto.Message) *ir.Metadata {
	value := message.ProtoReflect()
	field := value.Descriptor().Fields().ByName("meta")

	if field == nil || !value.Has(field) {
		return nil
	}

	switch meta := value.Get(field).Message().Interface().(type) {
	case *ir.Metadata:
		return meta
	default:
		return nil
	}
}

func refusal(code string, meta *ir.Metadata, message string) error {
	return &Error{Code: code, Message: message, Source: meta.GetSource()}
}

func position(span *ir.SourceSpan) string {
	return fmt.Sprintf("%s:%d:%d", span.GetFile(), span.GetStartLine(), span.GetStartColumn())
}
func objectKey(ref *ir.ObjectRef) string { return ref.GetKind().String() + "/" + ObjectID(ref) }
