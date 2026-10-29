package ddl

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/pkg/options"
)

// table is one CREATE TABLE statement and the CREATE INDEX statements after it.
type table struct {
	origin   string // proto full name of the message
	message  string // @go.message annotation
	embeds   []string
	name     string // schema-qualified SQL name
	elements []element
	indexes  []string
}

// element is one item of a CREATE TABLE body: a column or a table constraint.
type element struct {
	name    string // column name; empty for a table constraint
	origin  string // proto path of a column; empty for a table constraint
	goField string // @go.field or @go.case annotation of a column
	body    string
	trailer string // annotation written on the same line, if any
}

// fieldCtx is what a field inherits from the message or embed field holding it.
type fieldCtx struct {
	path     string                  // proto path of the holder
	goPath   string                  // Go field path of the holder, with a trailing dot
	prefix   string                  // column-name prefix from enclosing embeds
	nullable bool                    // an enclosing field has presence, so its columns may be NULL
	embeds   []protoreflect.FullName // messages on the embed chain, for cycle detection
	goEmbeds *[]string               // @go.embed annotations of the table
}

// buildTable maps one table message to its statement.
func (r registry) buildTable(msg *protogen.Message) (table, error) {
	desc := msg.Desc
	opt := option[*options.Table](desc.Options(), options.E_Table)
	origin := string(desc.FullName())

	var goEmbeds []string

	elements, err := r.messageColumns(msg, fieldCtx{
		path: origin, embeds: []protoreflect.FullName{desc.FullName()}, goEmbeds: &goEmbeds,
	})
	if err != nil {
		return table{}, err
	}

	if len(elements) == 0 {
		return table{}, fmt.Errorf("%w: %s has no columns", ErrInvalidOption, origin)
	}

	if err := checkNames(elements); err != nil {
		return table{}, err
	}

	keys, err := keyElements(desc, opt)
	if err != nil {
		return table{}, fmt.Errorf("%s: %w", origin, err)
	}

	indexes, err := indexStatements(desc, opt, qualifiedTable(desc))
	if err != nil {
		return table{}, fmt.Errorf("%s: %w", origin, err)
	}

	return table{
		origin:   origin,
		message:  "-- @go.message " + r.goIdent(msg.GoIdent),
		embeds:   goEmbeds,
		name:     qualifiedTable(desc),
		elements: append(elements, keys...),
		indexes:  indexes,
	}, nil
}

// render writes the statements of the table: its comment-annotated CREATE
// TABLE followed by the indexes.
func (t table) render(out *strings.Builder) {
	fmt.Fprintf(out, "\n-- @ratel.origin %s\n%s\n", t.origin, t.message)

	for _, embed := range t.embeds {
		out.WriteString(embed + "\n")
	}

	fmt.Fprintf(out, "CREATE TABLE %s (\n", t.name)

	for index, elem := range t.elements {
		if elem.origin != "" {
			fmt.Fprintf(out, "\t-- @ratel.origin %s\n", elem.origin)
		}

		if elem.goField != "" {
			out.WriteString("\t" + elem.goField + "\n")
		}

		out.WriteString("\t" + elem.body)

		if index < len(t.elements)-1 {
			out.WriteByte(',')
		}

		if elem.trailer != "" {
			out.WriteString(" " + elem.trailer)
		}

		out.WriteByte('\n')
	}

	out.WriteString(");\n")

	for _, statement := range t.indexes {
		out.WriteString(statement + "\n")
	}
}

// messageColumns maps the fields of a message, in declaration order, to
// columns. A oneof with embed adds its case column after its last member.
func (r registry) messageColumns(msg *protogen.Message, ctx fieldCtx) ([]element, error) {
	elements := make([]element, 0, len(msg.Fields))

	for _, field := range msg.Fields {
		fields, err := r.fieldElements(field, ctx)
		if err != nil {
			return nil, err
		}

		elements = append(elements, fields...)

		if oneofCase, ok := caseElement(field, ctx); ok {
			elements = append(elements, oneofCase)
		}
	}

	return elements, nil
}

// caseElement builds the case column of an embedded oneof, emitted once after
// the oneof's last member.
func caseElement(field *protogen.Field, ctx fieldCtx) (element, bool) {
	oneof := field.Oneof
	if oneof == nil || oneof.Desc.IsSynthetic() || oneof.Fields[len(oneof.Fields)-1] != field {
		return element{}, false
	}

	if !option[*options.Oneof](oneof.Desc.Options(), options.E_Oneof).GetEmbed() {
		return element{}, false
	}

	name := ctx.prefix + snakeCase(string(oneof.Desc.Name())) + "_case"
	labels := make([]string, 0, len(oneof.Fields))

	for _, member := range oneof.Fields {
		labels = append(labels, sqlString(string(member.Desc.Name())))
	}

	col := quoteIdent(name)

	return element{
		name:    name,
		origin:  ctx.path + "." + string(oneof.Desc.Name()),
		goField: goCase(ctx.goPath + oneof.GoName),
		body:    col + " text CHECK (" + col + " IN (" + strings.Join(labels, ", ") + "))",
	}, true
}

// fieldElements maps one field to its columns: none when skipped, the inlined
// columns of an embedded message, or a single value column.
func (r registry) fieldElements(field *protogen.Field, ctx fieldCtx) ([]element, error) {
	opt := option[*options.Column](field.Desc.Options(), options.E_Column)
	if opt.GetSkip() {
		return nil, nil
	}

	origin := ctx.path + "." + string(field.Desc.Name())

	if opt.GetEmbed() != nil {
		elements, err := r.embedColumns(field, opt, origin, ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", origin, err)
		}

		return elements, nil
	}

	el, err := r.valueColumn(field, opt, origin, ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", origin, err)
	}

	return []element{el}, nil
}

// embedColumns inlines the columns of a single message field. Its columns are
// nullable because the message itself has presence.
func (r registry) embedColumns(
	field *protogen.Field, opt *options.Column, origin string, ctx fieldCtx,
) ([]element, error) {
	if field.Message == nil || field.Desc.IsList() || field.Desc.IsMap() {
		return nil, fmt.Errorf("%w: embed needs a single message field", ErrInvalidOption)
	}

	name := field.Message.Desc.FullName()

	if _, ok := wellKnownType(name); ok {
		return nil, fmt.Errorf("%w: embed cannot inline %s", ErrInvalidOption, name)
	}

	if _, ok := wrapperKind(name); ok {
		return nil, fmt.Errorf("%w: embed cannot inline %s", ErrInvalidOption, name)
	}

	if opt.GetSerialize() || opt.GetType() != "" || opt.GetReferences() != nil ||
		opt.GetDefault() != "" || opt.GetGenerated() != "" || opt.GetName() != "" {
		return nil, fmt.Errorf("%w: embed excludes name, type, serialize, references, default and generated",
			ErrInvalidOption)
	}

	if slices.Contains(ctx.embeds, name) {
		return nil, fmt.Errorf("%w: %s embeds itself through %s", ErrEmbedCycle, name, origin)
	}

	*ctx.goEmbeds = append(*ctx.goEmbeds, r.goEmbed(field, ctx.goPath+field.GoName))

	inner := fieldCtx{
		path:     origin,
		goPath:   ctx.goPath + field.GoName + ".",
		prefix:   ctx.prefix + opt.GetEmbed().GetPrefix(),
		nullable: ctx.nullable || field.Desc.HasPresence(),
		embeds:   append(slices.Clip(ctx.embeds), name),
		goEmbeds: ctx.goEmbeds,
	}

	return r.messageColumns(field.Message, inner)
}

// valueColumn maps a field to one column definition with its constraints.
func (r registry) valueColumn(
	field *protogen.Field, opt *options.Column, origin string, ctx fieldCtx,
) (element, error) {
	name := ctx.prefix + columnName(field.Desc, opt)
	quoted := quoteIdent(name)

	if override := opt.GetType(); override != "" {
		if err := validateExpr("type", override); err != nil {
			return element{}, err
		}
	}

	spec, err := columnType(field, opt, quoted)
	if err != nil {
		return element{}, err
	}

	body := quoted + " " + spec.typ
	if !field.Desc.HasPresence() && !ctx.nullable {
		body += " NOT NULL"
	}

	clauses, err := r.constraintClauses(field, opt)
	if err != nil {
		return element{}, err
	}

	body += clauses.body
	if spec.check != "" {
		body += " CHECK (" + spec.check + ")"
	}

	goPath := ctx.goPath + field.GoName
	if oneof := field.Oneof; oneof != nil && !oneof.Desc.IsSynthetic() {
		goPath = ctx.goPath + oneof.GoName + "." + field.GoName
	}

	return element{
		name: name, origin: origin, goField: r.goField(field, opt, goPath), body: body, trailer: clauses.trailer,
	}, nil
}

// clauses are the optional column clauses and the annotation they carry.
type clauses struct {
	body    string
	trailer string
}

// constraintClauses renders DEFAULT, GENERATED and REFERENCES for a column.
func (r registry) constraintClauses(field *protogen.Field, opt *options.Column) (clauses, error) {
	var out clauses

	if def := opt.GetDefault(); def != "" {
		if err := validateExpr("default", def); err != nil {
			return clauses{}, err
		}

		if opt.GetGenerated() != "" {
			return clauses{}, fmt.Errorf("%w: a generated column takes no default", ErrInvalidOption)
		}

		out.body += " DEFAULT " + def
	}

	if gen := opt.GetGenerated(); gen != "" {
		if err := validateExpr("generated", gen); err != nil {
			return clauses{}, err
		}

		out.body += " GENERATED ALWAYS AS (" + gen + ") STORED"
	}

	if ref := opt.GetReferences(); ref != nil {
		if field.Desc.IsList() || field.Desc.IsMap() {
			return clauses{}, fmt.Errorf("%w: references needs a single column field", ErrInvalidOption)
		}

		fkClause, err := r.reference(ref)
		if err != nil {
			return clauses{}, err
		}

		out.body += " " + fkClause.body
		out.trailer = fkClause.trailer
	}

	return out, nil
}

// reference renders a foreign key to a table message and its @ratel.rel
// annotation. The referenced column is a direct field of the target.
func (r registry) reference(ref *options.Ref) (clauses, error) {
	target, ok := r.messages[protoreflect.FullName(ref.GetTable())]
	if !ok {
		return clauses{}, fmt.Errorf("%w: %s", ErrUnknownMessage, ref.GetTable())
	}

	if option[*options.Table](target.Desc.Options(), options.E_Table) == nil {
		return clauses{}, fmt.Errorf("%w: %s", ErrNotTable, ref.GetTable())
	}

	if len(ref.GetColumns()) != 1 {
		return clauses{}, fmt.Errorf("%w: a reference names exactly one column", ErrInvalidOption)
	}

	column, err := directColumnName(target.Desc, ref.GetColumns()[0])
	if err != nil {
		return clauses{}, err
	}

	trailer, err := relAnnotation(ref)
	if err != nil {
		return clauses{}, err
	}

	return clauses{
		body:    "REFERENCES " + qualifiedTable(target.Desc) + " (" + quoteIdent(column) + ")",
		trailer: trailer,
	}, nil
}

// relAnnotation renders @ratel.rel for a reference that names a relation.
func relAnnotation(ref *options.Ref) (string, error) {
	rel, inverse := ref.GetRel(), ref.GetInverse()

	if rel == "" {
		if inverse != "" {
			return "", fmt.Errorf("%w: inverse needs rel", ErrInvalidOption)
		}

		return "", nil
	}

	if !exported(rel) || (inverse != "" && !exported(inverse)) {
		return "", fmt.Errorf("%w: rel and inverse must be exported identifiers", ErrInvalidOption)
	}

	annotation := "-- @ratel.rel name=" + rel
	if inverse != "" {
		annotation += " inverse=" + inverse
	}

	return annotation, nil
}

// directColumnName returns the column of a direct, non-embedded field.
func directColumnName(msg protoreflect.MessageDescriptor, field string) (string, error) {
	desc := msg.Fields().ByName(protoreflect.Name(field))
	if desc == nil {
		return "", fmt.Errorf("%w: %s.%s", ErrUnknownField, msg.FullName(), field)
	}

	opt := option[*options.Column](desc.Options(), options.E_Column)
	if opt.GetSkip() || opt.GetEmbed() != nil {
		return "", fmt.Errorf("%w: %s.%s does not map to one column", ErrInvalidOption, msg.FullName(), field)
	}

	return columnName(desc, opt), nil
}

// directColumns resolves field names of a table message to column names.
func directColumns(msg protoreflect.MessageDescriptor, fields []string) ([]string, error) {
	columns := make([]string, 0, len(fields))

	for _, field := range fields {
		column, err := directColumnName(msg, field)
		if err != nil {
			return nil, err
		}

		columns = append(columns, column)
	}

	return columns, nil
}

// checkNames rejects two columns with the same name.
func checkNames(elements []element) error {
	owners := make(map[string]string, len(elements))

	for _, elem := range elements {
		if elem.name == "" {
			continue
		}

		if prev, ok := owners[elem.name]; ok {
			return fmt.Errorf("%w: column %q is produced by %s and %s",
				ErrDuplicateColumn, elem.name, prev, elem.origin)
		}

		owners[elem.name] = elem.origin
	}

	return nil
}
