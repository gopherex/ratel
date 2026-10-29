package mapper

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/pkg/ir"
)

// MapCreateTable maps a declaration without inventing implicit sequences or names.
func (m Mapper) MapCreateTable(value *pg.CreateStmt) *ir.Table {
	if value == nil {
		return nil
	}

	result := &ir.Table{
		Name:        relationName(value.GetRelation()),
		Persistence: persistence(value.GetRelation().GetRelpersistence()),
	}

	for _, node := range value.GetTableElts() {
		if column := node.GetColumnDef(); column != nil {
			result.Columns = append(result.Columns, m.column(column, boundedUint(len(result.GetColumns())+1)))

			for _, entry := range column.GetConstraints() {
				if constraint := m.constraint(entry.GetConstraint(), column.GetColname()); constraint != nil {
					result.Constraints = append(result.Constraints, constraint)
				}
			}
		} else if constraint := m.constraint(node.GetConstraint(), ""); constraint != nil {
			result.Constraints = append(result.Constraints, constraint)
		}
	}

	for _, node := range value.GetConstraints() {
		if constraint := m.constraint(node.GetConstraint(), ""); constraint != nil {
			result.Constraints = append(result.Constraints, constraint)
		}
	}

	for _, constraint := range result.GetConstraints() {
		for _, column := range result.GetColumns() {
			if slices.Contains(constraint.GetPrimaryKey().GetColumns(), column.GetName()) {
				column.Nullable = false
			}
		}
	}

	if len(value.GetInhRelations()) > 0 && value.GetPartbound() != nil {
		result.PartitionOf = relationName(value.GetInhRelations()[0].GetRangeVar())
	}

	result.PartitionBy = m.partitionBy(value)
	result.PartitionBound = m.partitionBound(value)
	slices.SortStableFunc(result.GetConstraints(), func(left, right *ir.Constraint) int {
		return cmp.Compare(left.GetName(), right.GetName())
	})
	m.finish(result)

	return result
}

func persistence(value string) ir.TablePersistence {
	switch value {
	case "u":
		return ir.TablePersistence_TABLE_PERSISTENCE_UNLOGGED
	case "t":
		return ir.TablePersistence_TABLE_PERSISTENCE_TEMPORARY
	default:
		return ir.TablePersistence_TABLE_PERSISTENCE_PERMANENT
	}
}

func (m Mapper) partitionBy(value *pg.CreateStmt) string {
	if value.GetPartspec() == nil {
		return ""
	}

	synthetic := &pg.CreateStmt{Relation: &pg.RangeVar{Relname: "t"}, Partspec: value.GetPartspec()}
	sql := m.statementSQL(&pg.Node{Node: &pg.Node_CreateStmt{CreateStmt: synthetic}})
	_, clause, _ := strings.Cut(sql, " PARTITION BY ")

	return clause
}

func (m Mapper) partitionBound(value *pg.CreateStmt) string {
	bound := ""

	if value.GetPartbound() != nil {
		synthetic := &pg.CreateStmt{
			Relation: &pg.RangeVar{Relname: "t"}, Partbound: value.GetPartbound(),
			InhRelations: []*pg.Node{{Node: &pg.Node_RangeVar{RangeVar: &pg.RangeVar{Relname: "p"}}}},
		}
		sql := m.statementSQL(&pg.Node{Node: &pg.Node_CreateStmt{CreateStmt: synthetic}})

		_, bound, _ = strings.Cut(sql, " FOR VALUES ")
		if bound != "" {
			bound = "FOR VALUES " + bound
		}

		if value.GetPartbound().GetIsDefault() {
			bound = "DEFAULT"
		}
	}

	return bound
}

// MapColumn maps one column; position is its one-based attribute position.
func (m Mapper) MapColumn(value *pg.ColumnDef, position uint32) *ir.Column {
	if value == nil {
		return nil
	}

	result := m.column(value, position)
	m.finish(result)

	return result
}

func (m Mapper) column(value *pg.ColumnDef, position uint32) *ir.Column {
	result := &ir.Column{
		Name: value.GetColname(), Position: position, Type: MapType(value.GetTypeName()),
		Nullable: !value.GetIsNotNull(), Collation: dotted(value.GetCollClause().GetCollname()),
		Meta: &ir.Metadata{Source: m.span(int(value.GetLocation()), m.end)},
	}

	for _, node := range value.GetConstraints() {
		entry := node.GetConstraint()
		switch entry.GetContype() {
		case pg.ConstrType_CONSTR_NOTNULL, pg.ConstrType_CONSTR_PRIMARY:
			result.Nullable = false
		case pg.ConstrType_CONSTR_DEFAULT:
			result.DefaultExpr = m.expr(entry.GetRawExpr())
		case pg.ConstrType_CONSTR_IDENTITY:
			result.Nullable = false

			kind := ir.IdentityKind_IDENTITY_KIND_ALWAYS
			if entry.GetGeneratedWhen() == "d" {
				kind = ir.IdentityKind_IDENTITY_KIND_BY_DEFAULT
			}

			result.Identity = &ir.Identity{Kind: kind, Options: m.sequenceOptions(entry.GetOptions())}
		case pg.ConstrType_CONSTR_GENERATED:
			// pg_query v6 is PG 17: CONSTR_GENERATED is always STORED. PG 18's
			// virtual/storage discriminator is not present in this AST contract.
			result.Generated = &ir.GeneratedColumn{
				Expression: m.expr(entry.GetRawExpr()),
				Storage:    ir.GeneratedStorage_GENERATED_STORAGE_STORED,
			}
		default:
		}
	}

	return result
}

// MapConstraint maps a table or column constraint; column supplies implicit keys.
// DEFAULT, IDENTITY and GENERATED are column properties, not standalone constraints.
func (m Mapper) MapConstraint(value *pg.Constraint, column string) *ir.Constraint {
	result := m.constraint(value, column)
	if result != nil {
		m.finish(result)
	}

	return result
}

func (m Mapper) constraint(value *pg.Constraint, column string) *ir.Constraint {
	if value == nil {
		return nil
	}

	result := &ir.Constraint{
		Name: value.GetConname(), Deferrable: value.GetDeferrable(),
		InitiallyDeferred: value.GetInitdeferred(), Validated: !value.GetSkipValidation(),
		Meta: &ir.Metadata{Source: m.span(int(value.GetLocation()), m.end)},
	}

	keys := stringsOf(value.GetKeys())
	if len(keys) == 0 && column != "" {
		keys = []string{column}
	}

	switch value.GetContype() {
	case pg.ConstrType_CONSTR_PRIMARY:
		result.Type = ir.ConstraintType_CONSTRAINT_TYPE_PRIMARY_KEY
		result.Body = &ir.Constraint_PrimaryKey{PrimaryKey: &ir.PrimaryKey{Columns: keys, Index: value.GetIndexname()}}
	case pg.ConstrType_CONSTR_UNIQUE:
		result.Type = ir.ConstraintType_CONSTRAINT_TYPE_UNIQUE
		result.Body = &ir.Constraint_Unique{Unique: &ir.UniqueConstraint{
			Columns: keys,
			Index:   value.GetIndexname(), NullsNotDistinct: value.GetNullsNotDistinct(),
		}}
	case pg.ConstrType_CONSTR_FOREIGN:
		result.Type = ir.ConstraintType_CONSTRAINT_TYPE_FOREIGN_KEY

		columns := stringsOf(value.GetFkAttrs())
		if len(columns) == 0 {
			columns = keys
		}

		result.Body = &ir.Constraint_ForeignKey{ForeignKey: &ir.ForeignKey{
			Columns:         columns,
			ReferencedTable: tableObject(value.GetPktable()), ReferencedColumns: stringsOf(value.GetPkAttrs()),
			OnDelete: referentialAction(value.GetFkDelAction()), OnUpdate: referentialAction(value.GetFkUpdAction()),
			MatchType: matchType(value.GetFkMatchtype()), ReferencedActionColumns: stringsOf(value.GetFkDelSetCols()),
		}}
	case pg.ConstrType_CONSTR_CHECK:
		result.Type = ir.ConstraintType_CONSTRAINT_TYPE_CHECK
		result.Body = &ir.Constraint_Check{Check: &ir.CheckConstraint{
			Expression: m.expr(value.GetRawExpr()), NoInherit: value.GetIsNoInherit(),
		}}
	case pg.ConstrType_CONSTR_NOTNULL:
		result.Type = ir.ConstraintType_CONSTRAINT_TYPE_NOT_NULL

		if len(keys) > 0 {
			column = keys[0]
		}

		result.Body = &ir.Constraint_NotNullColumn{NotNullColumn: column}
	case pg.ConstrType_CONSTR_EXCLUSION:
		result.Type = ir.ConstraintType_CONSTRAINT_TYPE_EXCLUSION
		result.Body = &ir.Constraint_Exclusion{Exclusion: m.exclusion(value)}
	default:
		return nil
	}

	return result
}

func (m Mapper) exclusion(value *pg.Constraint) *ir.ExclusionConstraint {
	result := &ir.ExclusionConstraint{IndexMethod: value.GetAccessMethod(), Predicate: m.expr(value.GetWhereClause())}

	for _, node := range value.GetExclusions() {
		pair := node.GetList().GetItems()

		const exclusionPairSize = 2

		if len(pair) < exclusionPairSize {
			continue
		}

		element := pair[0].GetIndexElem()
		mapped := &ir.ExclusionElement{Operator: dotted(pair[1].GetList().GetItems()), Opclass: dotted(element.GetOpclass())}

		if element.GetName() != "" {
			mapped.Target = &ir.ExclusionElement_Column{Column: element.GetName()}
		} else {
			mapped.Target = &ir.ExclusionElement_Expr{Expr: m.expr(element.GetExpr())}
		}

		result.Elements = append(result.Elements, mapped)
	}

	return result
}

func referentialAction(value string) ir.ReferentialAction {
	switch value {
	case "a":
		return ir.ReferentialAction_REFERENTIAL_ACTION_NO_ACTION
	case "r":
		return ir.ReferentialAction_REFERENTIAL_ACTION_RESTRICT
	case "c":
		return ir.ReferentialAction_REFERENTIAL_ACTION_CASCADE
	case "n":
		return ir.ReferentialAction_REFERENTIAL_ACTION_SET_NULL
	case "d":
		return ir.ReferentialAction_REFERENTIAL_ACTION_SET_DEFAULT
	default:
		return ir.ReferentialAction_REFERENTIAL_ACTION_UNSPECIFIED
	}
}

func matchType(value string) ir.MatchType {
	switch value {
	case "s":
		return ir.MatchType_MATCH_TYPE_SIMPLE
	case "f":
		return ir.MatchType_MATCH_TYPE_FULL
	case "p":
		return ir.MatchType_MATCH_TYPE_PARTIAL
	default:
		return ir.MatchType_MATCH_TYPE_UNSPECIFIED
	}
}

// MapIndex maps a declarative index. Concurrent-build state is not syntax state.
func (m Mapper) MapIndex(value *pg.IndexStmt) *ir.Index {
	if value == nil {
		return nil
	}

	result := &ir.Index{
		Name: value.GetIdxname(), Method: value.GetAccessMethod(), Unique: value.GetUnique(),
		Primary: value.GetPrimary(), NullsNotDistinct: value.GetNullsNotDistinct(), Valid: true, Ready: true,
		Predicate: m.expr(value.GetWhereClause()),
	}
	for _, node := range value.GetIndexParams() {
		result.Elements = append(result.Elements, m.indexElement(node.GetIndexElem()))
	}

	for _, node := range value.GetIndexIncludingParams() {
		result.Include = append(result.Include, node.GetIndexElem().GetName())
	}

	m.finish(result)

	return result
}

func (m Mapper) indexElement(value *pg.IndexElem) *ir.IndexElement {
	result := &ir.IndexElement{
		Collation: dotted(value.GetCollation()), Opclass: dotted(value.GetOpclass()),
		Order: sortOrder(value.GetOrdering()), Nulls: nullsOrder(value.GetNullsOrdering()),
	}
	if value.GetName() != "" {
		result.Target = &ir.IndexElement_Column{Column: value.GetName()}
	} else {
		result.Target = &ir.IndexElement_Expr{Expr: m.expr(value.GetExpr())}
	}

	return result
}

// MapView maps an ordinary view and its SELECT body.
func (m Mapper) MapView(value *pg.ViewStmt) *ir.View {
	if value == nil {
		return nil
	}

	result := &ir.View{
		Name: relationName(value.GetView()), Columns: stringsOf(value.GetAliases()),
		Query: m.selectStmt(value.GetQuery().GetSelectStmt()),
	}

	switch value.GetWithCheckOption() {
	case pg.ViewCheckOption_LOCAL_CHECK_OPTION:
		result.CheckOption = ir.CheckOption_CHECK_OPTION_LOCAL
	case pg.ViewCheckOption_CASCADED_CHECK_OPTION:
		result.CheckOption = ir.CheckOption_CHECK_OPTION_CASCADED
	default:
	}

	for _, node := range value.GetOptions() {
		option := node.GetDefElem()
		if option.GetDefname() == "security_barrier" {
			result.SecurityBarrier = booleanValue(option.GetArg())
		}
	}

	m.finish(result)

	return result
}

// MapCreateEnum preserves labels, including the valid empty-string label.
func (m Mapper) MapCreateEnum(value *pg.CreateEnumStmt) *ir.EnumType {
	if value == nil {
		return nil
	}

	result := &ir.EnumType{Name: qualified(value.GetTypeName()), Labels: stringsOf(value.GetVals())}
	m.finish(result)

	return result
}

// MapCreateSequence preserves explicit options; server defaults require a target version.
func (m Mapper) MapCreateSequence(value *pg.CreateSeqStmt) *ir.Sequence {
	if value == nil {
		return nil
	}

	result := m.sequenceOptions(value.GetOptions())
	result.Name = relationName(value.GetSequence())
	m.finish(result)

	return result
}

func (m Mapper) sequenceOptions(nodes []*pg.Node) *ir.Sequence {
	result := &ir.Sequence{}

	for _, node := range nodes {
		option := node.GetDefElem()
		arg := option.GetArg()

		switch option.GetDefname() {
		case "start":
			result.Start = integerValue(arg)
		case "increment":
			result.Increment = integerValue(arg)
		case "minvalue":
			result.MinValue = integerValue(arg)
		case "maxvalue":
			result.MaxValue = integerValue(arg)
		case "cache":
			result.Cache = integerValue(arg)
		case "cycle":
			result.Cycle = booleanValue(arg)
		case "as":
			result.DataType = MapType(arg.GetTypeName())
		case "owned_by":
			result.OwnedBy = &ir.ObjectRef{Kind: ir.ObjectKind_OBJECT_KIND_COLUMN, Name: qualified(arg.GetList().GetItems())}
		}
	}

	return result
}

func integerValue(node *pg.Node) int64 {
	switch {
	case node.GetInteger() != nil:
		return int64(node.GetInteger().GetIval())
	case node.GetAConst().GetIval() != nil:
		return int64(node.GetAConst().GetIval().GetIval())
	default:
		text := node.GetFloat().GetFval()
		if text == "" {
			text = node.GetAConst().GetFval().GetFval()
		}

		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0
		}

		return value
	}
}

func booleanValue(node *pg.Node) bool {
	switch {
	case node == nil:
		return true
	case node.GetBoolean() != nil:
		return node.GetBoolean().GetBoolval()
	case node.GetAConst().GetBoolval() != nil:
		return node.GetAConst().GetBoolval().GetBoolval()
	case node.GetString_() != nil:
		value := node.GetString_().GetSval()

		return value == "true" || value == "on"
	default:
		return integerValue(node) != 0
	}
}
