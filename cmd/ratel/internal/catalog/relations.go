package catalog

import (
	"cmp"
	"fmt"
	"go/token"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gopherex/ratel/ir"
)

// Diagnostic codes of the relation graph (docs/ir.md, Relations).
const (
	relationAmbiguous = "RTL-C05"
	relationConflict  = "RTL-C06"
	relationUnnamed   = "RTL-C07"
	relationCollision = "RTL-C08"
)

const (
	namespaceCore   = "ratel"
	annotationRel   = "rel"
	annotationNoRel = "norel"
	argumentName    = "name"
	argumentInverse = "inverse"
)

// edge is a foreign key of the catalog, with the relation annotation that names it.
type edge struct {
	table      *ir.Table
	constraint *ir.Constraint
	key        string
	pair       string
	columns    []string
	referenced *ir.QualifiedName
	targets    []string
	unique     bool
	norel      bool
	annotation *ir.AnnotationValue
}

// related is a derived relation with the position its diagnostics point to.
type related struct {
	relation *ir.Relation
	span     *ir.SourceSpan
}

type relator struct {
	diagnostics []*ir.Diagnostic
}

// joinKeyCount is the number of foreign keys that form a join table's primary key.
const joinKeyCount = 2

// relate derives Catalog.Relations from foreign keys and their annotations. It
// runs after annotations are attached. Problems are ERROR diagnostics, so every
// problem is reported in one run.
func relate(target *ir.Catalog) {
	work := &relator{}
	edges := work.edges(target)
	work.ambiguous(edges)

	relations := work.relations(edges)
	work.collisions(target, relations)

	target.Relations = make([]*ir.Relation, 0, len(relations))
	for _, item := range relations {
		target.Relations = append(target.Relations, item.relation)
	}

	target.Diagnostics = append(target.GetDiagnostics(), work.diagnostics...)
}

func (r *relator) fail(code string, span *ir.SourceSpan, message string) {
	r.diagnostics = append(r.diagnostics, &ir.Diagnostic{
		Severity: ir.DiagnosticSeverity_DIAGNOSTIC_SEVERITY_ERROR, Code: code, Message: message, Source: span,
	})
}

// edges lists the foreign keys sorted by object id. A foreign key with conflicting
// relation annotations is reported and left out.
func (r *relator) edges(c *ir.Catalog) []edge {
	var result []edge

	for _, schema := range c.GetSchemas() {
		for _, table := range schema.GetTables() {
			for _, constraint := range table.GetConstraints() {
				if constraint.GetForeignKey() == nil {
					continue
				}

				if candidate, ok := r.foreignKey(table, constraint); ok {
					result = append(result, candidate)
				}
			}
		}
	}

	slices.SortFunc(result, func(left, right edge) int { return cmp.Compare(left.key, right.key) })

	return result
}

func (r *relator) foreignKey(table *ir.Table, constraint *ir.Constraint) (edge, bool) {
	annotations := relationAnnotations(table, constraint)
	if len(annotations) > 1 {
		r.fail(relationConflict, constraint.GetMeta().GetSource(),
			"foreign key "+constraint.GetName()+" has more than one relation annotation")

		return edge{}, false
	}

	foreignKey := constraint.GetForeignKey()
	referenced := foreignKey.GetReferencedTable().GetName()
	pair := []string{ObjectID(tableRef(table.GetName())), ObjectID(tableRef(referenced))}
	slices.Sort(pair)

	result := edge{
		table:      table,
		constraint: constraint,
		key:        ObjectID(constraintRef(table.GetName(), constraint.GetName())),
		pair:       strings.Join(pair, "\x00"),
		columns:    foreignKey.GetColumns(),
		referenced: referenced,
		targets:    foreignKey.GetReferencedColumns(),
		unique:     isUnique(table, foreignKey.GetColumns()),
	}

	if len(annotations) == 1 {
		result.norel = annotations[0].GetName() == annotationNoRel
		result.annotation = annotations[0]
	}

	return result, true
}

// relationAnnotations returns the @ratel.rel and @ratel.norel annotations on a
// foreign key: on its constraint and on its local columns.
func relationAnnotations(table *ir.Table, constraint *ir.Constraint) []*ir.AnnotationValue {
	var found []*ir.AnnotationValue

	collect := func(meta *ir.Metadata) {
		for _, value := range meta.GetAnnotations() {
			if value.GetNamespace() == namespaceCore &&
				(value.GetName() == annotationRel || value.GetName() == annotationNoRel) {
				found = append(found, value)
			}
		}
	}

	collect(constraint.GetMeta())

	for _, column := range table.GetColumns() {
		if slices.Contains(constraint.GetForeignKey().GetColumns(), column.GetName()) {
			collect(column.GetMeta())
		}
	}

	return found
}

// isUnique reports whether columns are exactly the primary key or a unique constraint of table.
func isUnique(table *ir.Table, columns []string) bool {
	for _, constraint := range table.GetConstraints() {
		keyed := constraint.GetPrimaryKey().GetColumns()
		if constraint.GetUnique() != nil {
			keyed = constraint.GetUnique().GetColumns()
		}

		if len(keyed) > 0 && sameSet(keyed, columns) {
			return true
		}
	}

	return false
}

func sameSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	sortedLeft, sortedRight := slices.Clone(left), slices.Clone(right)
	slices.Sort(sortedLeft)
	slices.Sort(sortedRight)

	return slices.Equal(sortedLeft, sortedRight)
}

// ambiguous reports each foreign key that shares its pair of tables with another
// foreign key and has no @ratel.rel to tell them apart.
func (r *relator) ambiguous(edges []edge) {
	counts := make(map[string]int, len(edges))

	for _, item := range edges {
		if !item.norel {
			counts[item.pair]++
		}
	}

	for _, item := range edges {
		if item.norel || item.annotation != nil || counts[item.pair] < 2 {
			continue
		}

		r.fail(relationAmbiguous, item.constraint.GetMeta().GetSource(),
			"foreign key "+item.constraint.GetName()+" needs @ratel.rel: another foreign key connects the same tables")
	}
}

// relations derives one relation per foreign key, and one many-to-many relation per
// join table. The result is sorted by the foreign key, then by the join table.
func (r *relator) relations(edges []edge) []related {
	var result []related

	byTable := make(map[string][]edge)

	for _, item := range edges {
		if !item.norel {
			result = append(result, r.foreignKeyRelation(item))
		}

		id := ObjectID(tableRef(item.table.GetName()))
		byTable[id] = append(byTable[id], item)
	}

	for _, group := range byTable {
		keys := joinKeys(group[0].table, group)
		if len(keys) == joinKeyCount && !keys[0].norel && !keys[1].norel {
			result = append(result, r.manyToMany(keys[0], keys[1]))
		}
	}

	slices.SortFunc(result, func(left, right related) int {
		return cmp.Or(
			cmp.Compare(ObjectID(left.relation.GetFk()), ObjectID(right.relation.GetFk())),
			cmp.Compare(ObjectID(left.relation.GetThrough()), ObjectID(right.relation.GetThrough())),
		)
	})

	return result
}

// joinKeys returns the two foreign keys that together form the primary key of a join
// table. A join table has exactly two foreign keys inside its primary key, and they
// cover it. They are ordered by object id.
func joinKeys(table *ir.Table, group []edge) []edge {
	var primary []string

	for _, constraint := range table.GetConstraints() {
		if constraint.GetPrimaryKey() != nil {
			primary = constraint.GetPrimaryKey().GetColumns()
		}
	}

	if len(primary) == 0 {
		return nil
	}

	var inside []edge

	for _, item := range group {
		if subset(item.columns, primary) {
			inside = append(inside, item)
		}
	}

	if len(inside) != joinKeyCount {
		return nil
	}

	covered := append(slices.Clone(inside[0].columns), inside[1].columns...)
	if !sameSet(covered, primary) {
		return nil
	}

	slices.SortFunc(inside, func(left, right edge) int { return cmp.Compare(left.key, right.key) })

	return inside
}

func subset(columns, of []string) bool {
	for _, column := range columns {
		if !slices.Contains(of, column) {
			return false
		}
	}

	return true
}

// foreignKeyRelation builds the to-one relation from the referencing table and its
// to-many inverse on the referenced table, or a to-one inverse when the columns are unique.
func (r *relator) foreignKeyRelation(item edge) related {
	span := item.constraint.GetMeta().GetSource()

	name := argument(item.annotation, argumentName)
	if name == "" {
		name = r.derive(span, foreignKeyName(item.columns))
	}

	inverse := argument(item.annotation, argumentInverse)
	if inverse == "" {
		inverse = r.derive(span, exportedName(lastPart(item.table.GetName())))
	}

	return related{span: span, relation: &ir.Relation{
		Name:        name,
		InverseName: inverse,
		Kind:        ir.RelationKind_RELATION_KIND_ONE,
		InverseKind: inverseKind(item.unique),
		From:        endpoint(item.table.GetName(), item.columns),
		To:          endpoint(item.referenced, item.targets),
		Fk:          constraintRef(item.table.GetName(), item.constraint.GetName()),
	}}
}

// manyToMany builds the relation between the two referenced tables of a join table.
// The name of each join foreign key names the navigation to the table it references,
// so the name of the join key to B names A's navigation to B.
func (r *relator) manyToMany(first, second edge) related {
	span := first.constraint.GetMeta().GetSource()

	name := argument(second.annotation, argumentName)
	if name == "" {
		name = r.derive(span, exportedName(lastPart(second.referenced)))
	}

	inverse := argument(first.annotation, argumentName)
	if inverse == "" {
		inverse = r.derive(span, exportedName(lastPart(first.referenced)))
	}

	return related{span: span, relation: &ir.Relation{
		Name:        name,
		InverseName: inverse,
		Kind:        ir.RelationKind_RELATION_KIND_MANY,
		InverseKind: ir.RelationKind_RELATION_KIND_MANY,
		From:        endpoint(first.referenced, first.targets),
		To:          endpoint(second.referenced, second.targets),
		Through:     tableRef(first.table.GetName()),
		Fk:          constraintRef(first.table.GetName(), first.constraint.GetName()),
	}}
}

// derive returns a derived Go-facing name, and reports a failure when it is not an
// exported identifier.
func (r *relator) derive(span *ir.SourceSpan, name string) string {
	if !token.IsExported(name) || !token.IsIdentifier(name) {
		r.fail(relationUnnamed, span, fmt.Sprintf("cannot derive relation name %q; declare @ratel.rel name=<Name>", name))
	}

	return name
}

// collisions reports a relation name that is already taken on its table by a column
// or by another relation. Names are compared as exported Go identifiers.
func (r *relator) collisions(c *ir.Catalog, relations []related) {
	taken := make(map[string]string)

	for _, schema := range c.GetSchemas() {
		for _, table := range schema.GetTables() {
			for _, column := range table.GetColumns() {
				taken[ObjectID(tableRef(table.GetName()))+"\x00"+exportedName(column.GetName())] =
					"column " + column.GetName()
			}
		}
	}

	for _, item := range relations {
		origin := "relation " + ObjectID(item.relation.GetFk())
		r.claim(taken, item, item.relation.GetFrom().GetTable(), item.relation.GetName(), origin)
		r.claim(taken, item, item.relation.GetTo().GetTable(), item.relation.GetInverseName(), origin)
	}
}

func (r *relator) claim(taken map[string]string, item related, table *ir.ObjectRef, name, origin string) {
	key := ObjectID(table) + "\x00" + name

	if previous, exists := taken[key]; exists {
		r.fail(relationCollision, item.span, fmt.Sprintf(
			"relation name %s on %s collides with %s", name, ObjectID(table), previous,
		))

		return
	}

	taken[key] = origin
}

func inverseKind(unique bool) ir.RelationKind {
	if unique {
		return ir.RelationKind_RELATION_KIND_ONE
	}

	return ir.RelationKind_RELATION_KIND_MANY
}

// foreignKeyName derives the to-one name from the referencing columns: each column
// without its "_id" suffix, in PascalCase, concatenated in column order.
func foreignKeyName(columns []string) string {
	var name strings.Builder

	for _, column := range columns {
		base := strings.TrimSuffix(column, "_id")
		if base == "" {
			base = column
		}

		name.WriteString(exportedName(base))
	}

	return name.String()
}

// exportedName turns a snake_case identifier into PascalCase: each underscore-separated
// part is capitalized, and the rest of the part is kept as written.
func exportedName(snake string) string {
	var name strings.Builder

	for part := range strings.SplitSeq(snake, "_") {
		first, size := utf8.DecodeRuneInString(part)
		if size == 0 {
			continue
		}

		name.WriteRune(unicode.ToUpper(first))
		name.WriteString(part[size:])
	}

	return name.String()
}

func argument(value *ir.AnnotationValue, key string) string {
	for _, arg := range value.GetArgs() {
		if arg.GetKey() == key {
			return arg.GetValue()
		}
	}

	return ""
}

func lastPart(name *ir.QualifiedName) string {
	parts := name.GetParts()

	return parts[len(parts)-1]
}

func tableRef(name *ir.QualifiedName) *ir.ObjectRef {
	return &ir.ObjectRef{
		Kind: ir.ObjectKind_OBJECT_KIND_TABLE, Name: &ir.QualifiedName{Parts: slices.Clone(name.GetParts())},
	}
}

func constraintRef(table *ir.QualifiedName, name string) *ir.ObjectRef {
	parts := append(slices.Clone(table.GetParts()), name)

	return &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_CONSTRAINT, Name: &ir.QualifiedName{Parts: parts}}
}

func endpoint(table *ir.QualifiedName, columns []string) *ir.RelationEndpoint {
	return &ir.RelationEndpoint{Table: tableRef(table), Columns: slices.Clone(columns)}
}
