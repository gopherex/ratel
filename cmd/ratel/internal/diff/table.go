package diff

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

// setExpression is the first major version with ALTER COLUMN SET EXPRESSION.
const setExpression = 17

// alteredTable is the changes between two renderings of a table: its
// persistence, its place in a partition tree and its columns. A changed
// partition key has no statement altering it, so the table is replaced.
func alteredTable(current, desired introspect.Rendering, intents Intents, name, before, after string) delta {
	key := tableObject.key(name)

	if partitionKey(before) != partitionKey(after) {
		return delta{changes: replaced(key, before, after)}
	}

	oldParent, wasPartition := partitionParent(before)

	if _, isPartition := partitionParent(after); isPartition {
		return delta{changes: alteredPartition(current, desired, name, before, after)}
	}

	var result delta

	if wasPartition {
		result.changes = append(result.changes, Change{
			Kind: DetachPartition, Key: key, Table: oldParent, Before: before, After: after,
			SQL: "ALTER TABLE " + oldParent + " DETACH PARTITION " + name + ";",
		})
	}

	if logged := !strings.HasPrefix(after, "CREATE UNLOGGED "); logged != !strings.HasPrefix(before, "CREATE UNLOGGED ") {
		persistence := "SET UNLOGGED"
		if logged {
			persistence = "SET LOGGED"
		}

		result.changes = append(result.changes, Change{
			Kind: AlterPersistence, Key: key, Table: name, Before: before, After: after,
			SQL: "ALTER TABLE " + name + " " + persistence + ";",
		})
	}

	columns := alteredColumns(current, desired, intents, name)
	result.changes = append(result.changes, columns.changes...)
	result.drops = columns.drops

	return result
}

// alteredPartition moves a table to its desired parent and bound. A
// partition's columns are its parent's except for the names of its not-null
// constraints, which can only be renamed while it is detached.
func alteredPartition(current, desired introspect.Rendering, name, before, after string) []Change {
	key := tableObject.key(name)
	oldParent, wasPartition := partitionParent(before)
	newParent, _ := partitionParent(after)
	renames := partitionColumns(current, desired, name)

	var changes []Change

	if wasPartition && (before != after || len(renames) > 0) {
		changes = append(changes, Change{
			Kind: DetachPartition, Key: key, Table: oldParent, Before: before, After: after,
			SQL: "ALTER TABLE " + oldParent + " DETACH PARTITION " + name + ";",
		})
	}

	changes = append(changes, renames...)

	if !wasPartition || len(changes) > 0 {
		changes = append(changes, Change{
			Kind: AttachPartition, Key: key, Table: newParent, Before: before, After: after,
			SQL: attach(newParent, name, after),
		})
	}

	return changes
}

// partitionColumns renames the not-null constraints of a partition, the one
// column part it does not take from its parent.
func partitionColumns(current, desired introspect.Rendering, table string) []Change {
	edit := columnEdit{table: table, major: desired.Major}
	columns := columnsOf(desired, table)

	for index := range columns {
		previous, exists := current.Column(columns[index].Key)
		if exists && previous.NotNull != "" && columns[index].NotNull != "" {
			edit.name = columns[index].Key[len(table)+1:]
			notNullAltered(&edit, previous.NotNull, columns[index].NotNull)
		}
	}

	return edit.changes
}

// partitionKey is the PARTITION BY clause of a rendered table.
func partitionKey(sql string) string {
	_, key, _ := strings.Cut(sql, " PARTITION BY ")

	return key
}

// attach attaches a table as a partition with the bound its rendering
// states.
func attach(parent, name, sql string) string {
	_, bound, _ := strings.Cut(sql, " PARTITION OF "+parent+" ")
	bound, _, _ = strings.Cut(strings.TrimSuffix(bound, ";"), " PARTITION BY ")

	return "ALTER TABLE " + parent + " ATTACH PARTITION " + name + " " + bound + ";"
}

// columnKey is the change key of a "schema.table.column" column.
func columnKey(column string) string { return "column " + column }

// alteredColumns pairs the columns of a table by name, or by a rename
// intent, and compares each pair. Dropped columns go with the drops.
func alteredColumns(current, desired introspect.Rendering, intents Intents, table string) delta {
	var result delta

	taken := make(map[string]bool)
	columns := columnsOf(desired, table)

	for index := range columns {
		column := &columns[index]
		name := column.Key[len(table)+1:]
		previous, exists := current.Column(column.Key)

		if old, rename := intents.Renamed[columnKey(column.Key)]; !exists && rename {
			previous, exists = current.Column(table + "." + old)
			if exists {
				taken[previous.Key] = true

				result.changes = append(result.changes, Change{
					Kind: RenameColumn, Key: columnKey(column.Key), Table: table, Column: name,
					Before: previous.Definition, After: column.Definition,
					SQL: "ALTER TABLE " + table + " RENAME COLUMN " + old + " TO " + name + ";",
				})
			}
		}

		if !exists {
			result.changes = append(result.changes, Change{
				Kind: AddColumn, Key: columnKey(column.Key), Table: table, Column: name, After: column.Definition,
				SQL: "ALTER TABLE " + table + " ADD COLUMN " + column.Definition + ";",
			})

			continue
		}

		taken[previous.Key] = true
		edit := columnEdit{table: table, name: name, major: desired.Major}
		result.changes = append(result.changes, alteredColumn(edit, &previous, column, intents)...)
	}

	result.drops = droppedColumns(columnsOf(current, table), taken, table)

	return result
}

func droppedColumns(columns []introspect.Column, taken map[string]bool, table string) []Change {
	var drops []Change

	for index := range columns {
		if taken[columns[index].Key] {
			continue
		}

		name := columns[index].Key[len(table)+1:]
		drops = append(drops, Change{
			Kind: DropColumn, Key: columnKey(columns[index].Key), Table: table, Column: name,
			Before: columns[index].Definition, SQL: "ALTER TABLE " + table + " DROP COLUMN " + name + ";",
		})
	}

	return drops
}

// columnEdit builds the changes of one column on a server major version.
type columnEdit struct {
	table, name string
	major       int
	changes     []Change
}

func (e *columnEdit) add(kind Kind, before, after, sql string) {
	e.changes = append(e.changes, Change{
		Kind: kind, Key: columnKey(e.table + "." + e.name), Table: e.table, Column: e.name,
		Before: before, After: after, SQL: sql,
	})
}

// alter adds an ALTER COLUMN action.
func (e *columnEdit) alter(kind Kind, before, after, action string) {
	e.add(kind, before, after, "ALTER TABLE "+e.table+" ALTER COLUMN "+e.name+" "+action+";")
}

// alteredColumn is the changes from one column definition to another, in an
// order each statement accepts: what goes is removed before the type
// changes, what comes is added after.
func alteredColumn(edit columnEdit, before, after *introspect.Column, intents Intents) []Change {
	if !generationAltered(&edit, before, after) {
		edit.add(AlterGenerated, before.Definition, after.Definition, "ALTER TABLE "+edit.table+" DROP COLUMN "+
			edit.name+";\nALTER TABLE "+edit.table+" ADD COLUMN "+after.Definition+";")

		return edit.changes
	}

	if before.Identity != "" && after.Identity == "" {
		edit.alter(AlterIdentity, before.Identity, "", "DROP IDENTITY")
	}

	if before.Default != "" && after.Default == "" {
		edit.alter(DropDefault, before.Default, "", "DROP DEFAULT")
	}

	if before.Type != after.Type || before.Collation != after.Collation {
		retyped(&edit, before, after, intents.Retype[columnKey(edit.table+"."+edit.name)])
	}

	if after.Default != "" && before.Default != after.Default {
		edit.alter(SetDefault, before.Default, after.Default, "SET DEFAULT "+after.Default)
	}

	// An identity needs NOT NULL first.
	notNullAltered(&edit, before.NotNull, after.NotNull)

	switch {
	case after.Identity == "" || before.Identity == after.Identity:
	case before.Identity == "":
		edit.alter(AlterIdentity, "", after.Identity, "ADD"+after.Identity)
	default:
		edit.alter(AlterIdentity, before.Identity, after.Identity, identityOptions(after.Identity))
	}

	return edit.changes
}

// generationAltered adds the change of a generation expression that a
// statement can make, and reports false when the column must be dropped
// and added again: it becomes generated, changes its storage, or changes
// its expression before PostgreSQL 17 (no SET EXPRESSION).
func generationAltered(edit *columnEdit, before, after *introspect.Column) bool {
	if before.Generated == after.Generated {
		return true
	}

	storage := func(clause string) string { return clause[strings.LastIndex(clause, " ")+1:] }

	switch {
	case before.Generated == "":
		return false
	case after.Generated == "":
		// Only a stored expression can be dropped and keep its values.
		if storage(before.Generated) != "STORED" {
			return false
		}

		edit.alter(AlterGenerated, before.Generated, "", "DROP EXPRESSION")
	case storage(before.Generated) != storage(after.Generated) || edit.major < setExpression:
		return false
	default:
		expression := strings.TrimPrefix(after.Generated, " GENERATED ALWAYS")
		expression = expression[:strings.LastIndex(expression, " ")]
		edit.alter(AlterGenerated, before.Generated, after.Generated, "SET EXPRESSION"+expression)
	}

	return true
}

// retyped adds the type or collation change; an empty collation is the
// type's default.
func retyped(edit *columnEdit, before, after *introspect.Column, using string) {
	kind := AlterColumnType
	if before.Type == after.Type {
		kind = AlterCollation
	}

	action := "TYPE " + plainType(after.Type) + after.Collation
	if after.Collation == "" && before.Collation != "" {
		action += ` COLLATE pg_catalog."default"`
	}

	if using != "" {
		action += " USING " + using
	}

	edit.alter(kind, before.Type+before.Collation, after.Type+after.Collation, action)
}

// plainType is the type a serial pseudo-type stands for.
func plainType(name string) string {
	switch name {
	case "smallserial":
		return "smallint"
	case "serial":
		return "integer"
	case "bigserial":
		return "bigint"
	default:
		return name
	}
}

// identityOptions turns an identity clause " GENERATED <kind> AS IDENTITY
// (<options>)" into the ALTER COLUMN actions setting the same kind and
// options.
func identityOptions(clause string) string {
	kind, options, _ := strings.Cut(strings.TrimPrefix(clause, " GENERATED "), " AS IDENTITY (")
	options = strings.TrimSuffix(options, ")")
	options, cycle := strings.CutSuffix(options, " CYCLE")

	for _, option := range []string{"INCREMENT BY", "MINVALUE", "MAXVALUE", "CACHE"} {
		options = strings.Replace(options, " "+option+" ", " SET "+option+" ", 1)
	}

	actions := "SET GENERATED " + kind + " SET " + options
	if cycle {
		return actions + " SET CYCLE"
	}

	return actions + " SET NO CYCLE"
}

// notNullAltered adds the change between two NOT NULL clauses: set, drop,
// or a rename of the PostgreSQL 18 not-null constraint.
func notNullAltered(edit *columnEdit, before, after string) {
	name := func(clause string) string {
		return strings.TrimSuffix(strings.TrimPrefix(clause, " CONSTRAINT "), " NOT NULL")
	}

	switch {
	case before == after:
	case after == "":
		edit.alter(DropNotNull, before, "", "DROP NOT NULL")
	case before == "" && strings.HasPrefix(after, " CONSTRAINT "):
		edit.add(SetNotNull, "", after,
			"ALTER TABLE "+edit.table+" ADD CONSTRAINT "+name(after)+" NOT NULL "+edit.name+";")
	case before == "":
		edit.alter(SetNotNull, "", after, "SET NOT NULL")
	default:
		edit.add(RenameConstraint, before, after,
			"ALTER TABLE "+edit.table+" RENAME CONSTRAINT "+name(before)+" TO "+name(after)+";")
	}
}
