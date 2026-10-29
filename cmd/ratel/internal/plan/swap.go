package plan

import (
	"strings"

	"github.com/gopherex/ratel/pkg/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// retype plans a column type or collation change. Without a rewrite or an
// index rebuild it is one statement. Otherwise a second column of the new
// type is filled beside the old one, with copies of the old column's indexes
// and constraints built without blocking, and contract swaps them in one
// short transaction.
func (p *planner) retype(change *diff.Change) {
	table := change.Table
	kind := statement(change.SQL, safety.AlterColumnType, table, "")

	if !p.env.Safety.Exists(table) || !p.env.Rewrites[change.Key] && !p.env.Reindexes[change.Key] {
		p.short(change, kind)

		return
	}

	code := "RTL-R02"
	if !p.env.Rewrites[change.Key] {
		code = "RTL-R04"
	}

	using := p.intents.Retype[change.Key]
	if using == "" && change.Kind == diff.AlterColumnType {
		kind.Operations[0].Rewrite = true
		p.unsafe(change, code, "a rewriting type change needs @ratel.retype", kind)

		return
	}

	if using == "" {
		using = change.Column
	}

	dependents, ok := p.dependents(table, change.Column)
	key := p.primaryKey(table)

	if !ok || len(key) == 0 {
		kind.Operations[0].Rewrite = p.env.Rewrites[change.Key]
		p.unsafe(change, code, "the column is referenced by another table's foreign key or "+table+
			" has no primary key", kind)

		return
	}

	p.find(change, code, "a new column is filled beside the old one and swapped in", true)
	p.rebuilt[change.Key] = true
	p.swap(change, using, dependents, key)
}

// swap is the expand, backfill and contract steps of a column swap.
func (p *planner) swap(change *diff.Change, using string, dependents []introspect.Object, key []string) {
	table, column := change.Table, change.Column
	temporary := generated(column, "_ratel_new")
	desired, _ := p.env.Desired.Column(strings.TrimPrefix(change.Key, "column "))
	expression := replaceIdentifier(using, column, "NEW."+column)
	sync := p.sync(table, temporary, "NEW."+temporary+" := "+expression+";")

	p.add(change, migrate.PhaseExpand, migrate.ClassShort,
		append(p.newColumn(table, temporary, &desired), sync.create...)...)
	p.backfill(change, &Fill{
		Table: table, Key: key, Set: temporary + " = " + using,
		Where: temporary + " IS NULL AND " + column + " IS NOT NULL",
	})

	if desired.NotNull != "" {
		p.notNull(change, table, temporary, " NOT NULL", migrate.PhaseExpand)
	}

	swap := append([]Statement(nil), sync.drop...)
	after := p.copies(change, temporary, dependents)

	for _, object := range dependents {
		if user(kindOf(object.Key)) {
			swap = append(swap, dropped(object))
		}
	}

	swap = append(swap,
		statement("ALTER TABLE "+table+" DROP COLUMN "+column+";", safety.DropColumn, table, ""),
		statement("ALTER TABLE "+table+" RENAME COLUMN "+temporary+" TO "+column+";", safety.RenameColumn, table, ""))

	if desired.NotNull != "" && p.env.Safety.Major >= postgres18 {
		swap = append(swap, statement("ALTER TABLE "+table+" RENAME CONSTRAINT "+
			notNullName(table, temporary, " NOT NULL")+" TO "+notNullName(table, column, desired.NotNull)+";",
			safety.RenameConstraint, table, ""))
	}

	p.add(change, migrate.PhaseContract, migrate.ClassShort, append(swap, after...)...)
}

// copies builds, beside the old column, a copy of each index and constraint
// on it over the new column, and returns the statements of the swap that
// give the copies the old names and recreate the views, policies and
// triggers using the column.
func (p *planner) copies(change *diff.Change, temporary string, dependents []introspect.Object) []Statement {
	table, column := change.Table, change.Column

	var after []Statement

	for _, object := range dependents {
		name := lastPart(nameOf(object.Key))
		copied := replaceIdentifier(object.SQL, column, temporary)
		copyName := generated(name, "_ratel_new")

		switch kindOf(object.Key) {
		case indexObject:
			p.createIndex(&diff.Change{Kind: diff.CreateIndex, Key: object.Key, Table: table, SQL: copied},
				copyName, copied)

			after = append(after, statement("ALTER INDEX "+schemaOf(nameOf(object.Key))+"."+copyName+" RENAME TO "+
				name+";", safety.RenameIndex, schemaOf(nameOf(object.Key))+"."+copyName, ""))
		case constraintObject:
			copiedChange := diff.Change{
				Kind: constraintChange(copied), Key: object.Key, Table: table, SQL: copied, After: copied,
			}
			p.addConstraint(&copiedChange, copyName, "")

			after = append(after, statement("ALTER TABLE "+table+" RENAME CONSTRAINT "+copyName+" TO "+name+";",
				safety.RenameConstraint, table, ""))
		default:
			after = append(after, Statement{SQL: object.SQL, Operations: []safety.Operation{
				{Kind: recreateKind(kindOf(object.Key)), Relation: table},
			}})
		}
	}

	return after
}

// dependents are the indexes and constraints of a table and the views,
// policies and triggers that use one of its columns; false when another
// table's foreign key references the column, which a swap cannot move.
func (p *planner) dependents(table, column string) ([]introspect.Object, bool) {
	var result []introspect.Object

	for _, object := range p.env.Current.Objects {
		if kindOf(object.Key) == constraintObject && referenced(object.SQL) == table &&
			mentions(afterReferences(object.SQL), column) {
			return nil, false
		}

		if dependsOn(object, table, column) {
			result = append(result, object)
		}
	}

	return result, true
}

// Object kinds a swap moves: the table's own indexes and constraints, and
// the users of the column that are dropped and created again.
const (
	indexObject      = "index"
	constraintObject = "constraint"
	viewObject       = "view"
	policyObject     = "policy"
	triggerObject    = "trigger"
	routineObject    = "routine"
)

func user(kind string) bool {
	return kind == viewObject || kind == policyObject || kind == triggerObject
}

// dependsOn reports an object of the table using the column, or a view,
// policy or trigger naming both.
func dependsOn(object introspect.Object, table, column string) bool {
	if !mentions(object.SQL, column) {
		return false
	}

	switch kind := kindOf(object.Key); kind {
	case constraintObject:
		return strings.HasPrefix(nameOf(object.Key), table+".")
	case indexObject:
		return indexTable(object.SQL) == table
	default:
		return user(kind) && mentions(object.SQL, lastPart(table))
	}
}

// afterReferences is the referenced column list of a foreign key.
func afterReferences(sql string) string {
	_, rest, _ := strings.Cut(sql, " REFERENCES ")

	return rest
}

// dropped drops a view, policy or trigger for a swap.
func dropped(object introspect.Object) Statement {
	change := diff.Dropped(object)
	relation := change.Table

	if relation == "" {
		relation = nameOf(object.Key)
	}

	return Statement{SQL: change.SQL, Operations: []safety.Operation{{
		Kind:     dropKind(kindOf(object.Key)),
		Relation: relation,
	}}}
}

func recreateKind(kind string) safety.Kind {
	switch kind {
	case viewObject:
		return safety.CreateView
	case policyObject:
		return safety.CreatePolicy
	default:
		return safety.CreateTrigger
	}
}

func dropKind(kind string) safety.Kind {
	switch kind {
	case viewObject:
		return safety.DropView
	case policyObject:
		return safety.DropPolicy
	default:
		return safety.DropTrigger
	}
}

// constraintChange classifies a rendered ADD CONSTRAINT as the diff does.
func constraintChange(sql string) diff.Kind {
	return diff.Created(introspect.Object{Key: "constraint x.y.z", SQL: sql}).Kind
}

// indexTable is the table of a rendered CREATE INDEX.
func indexTable(sql string) string {
	_, rest, _ := strings.Cut(sql, " ON ")
	table, _, _ := strings.Cut(strings.TrimPrefix(rest, "ONLY "), " ")

	return table
}

// kindOf is the object kind of a rendering key.
func kindOf(key string) string {
	kind, _, _ := strings.Cut(key, " ")

	return kind
}

// mentions reports whether SQL uses an identifier outside string literals.
func mentions(sql, identifier string) bool {
	return replaceIdentifier(sql, identifier, "\x00") != sql
}

// replaceIdentifier replaces each use of an identifier, quoted as rendered,
// outside string literals and inside longer names only after a dot.
func replaceIdentifier(sql, identifier, replacement string) string {
	var out strings.Builder

	for index := 0; index < len(sql); {
		switch {
		case sql[index] == '\'':
			end := literalEnd(sql, index)
			out.WriteString(sql[index:end])

			index = end
		case strings.HasPrefix(sql[index:], identifier) && standsAlone(sql, index, len(identifier)):
			out.WriteString(replacement)

			index += len(identifier)
		default:
			out.WriteByte(sql[index])

			index++
		}
	}

	return out.String()
}

func standsAlone(sql string, index, length int) bool {
	before := index == 0 || !wordByte(sql[index-1]) || sql[index-1] == '.'
	end := index + length

	return before && (end == len(sql) || !wordByte(sql[end]))
}

func wordByte(b byte) bool {
	return b == '_' || b == '$' || b == '"' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' ||
		b >= 'A' && b <= 'Z' || b >= 0x80
}

// literalEnd is the index after the string literal starting at start.
func literalEnd(sql string, start int) int {
	for index := start + 1; index < len(sql); index++ {
		if sql[index] != '\'' {
			continue
		}

		if index+1 < len(sql) && sql[index+1] == '\'' {
			index++

			continue
		}

		return index + 1
	}

	return len(sql)
}
