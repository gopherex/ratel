package plan

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// column plans column changes.
func (p *planner) column(change *diff.Change) bool {
	table := change.Table

	if strings.HasPrefix(change.Key, "column ") && p.rebuilt[change.Key] {
		return true
	}

	switch change.Kind {
	case diff.AddColumn:
		p.addColumn(change)
	case diff.RenameColumn:
		p.renameColumn(change)
	case diff.AlterColumnType, diff.AlterCollation:
		p.retype(change)
	case diff.SetNotNull:
		if !p.env.Safety.Exists(table) {
			p.short(change, statement(change.SQL, safety.SetNotNull, table, ""))
		} else {
			p.notNull(change, table, change.Column, change.After, Expand)
		}
	case diff.AlterIdentity:
		identity := statement(change.SQL, safety.AlterIdentity, table, "")
		if p.notNulled[table+"."+change.Column] {
			// An identity is added once the column is NOT NULL, in contract.
			p.add(change, Contract, Short, identity)
		} else {
			p.short(change, identity)
		}
	case diff.AlterGenerated:
		p.generated(change)
	default:
		kind, ok := plainColumn(change.Kind)
		if ok {
			p.short(change, statement(change.SQL, kind, table, ""))
		}

		return ok
	}

	return true
}

// plainColumn is the operation of a column change applied as written.
func plainColumn(kind diff.Kind) (safety.Kind, bool) {
	switch kind {
	case diff.DropColumn:
		return safety.DropColumn, true
	case diff.DropNotNull:
		return safety.DropNotNull, true
	case diff.SetDefault:
		return safety.SetDefault, true
	case diff.DropDefault:
		return safety.DropDefault, true
	default:
		return safety.Unknown, false
	}
}

// addColumn adds a column as written when that is metadata only. A column
// whose default must be computed per row, or that is NOT NULL without a
// default, is added nullable, filled by its backfill intent, and then made
// NOT NULL in contract, where old code no longer inserts without it.
func (p *planner) addColumn(change *diff.Change) {
	table := change.Table
	desired, _ := p.env.Desired.Column(strings.TrimPrefix(change.Key, "column "))
	add := statement(change.SQL, safety.AddColumn, table, "")
	rewrite := p.env.Rewrites[change.Key]
	unfilled := desired.NotNull != "" && desired.Default == "" && desired.Identity == "" && desired.Generated == ""

	switch {
	case !p.env.Safety.Exists(table) || !rewrite && !unfilled:
		add.Operations[0].Rewrite = rewrite
		p.short(change, add)

		return
	case desired.Identity != "" || desired.Generated != "":
		add.Operations[0].Rewrite = true
		p.unsafe(change, "RTL-R01", "an identity or stored generated column is filled by a table rewrite", add)

		return
	}

	code := "RTL-C03"
	if rewrite {
		code = "RTL-R01"
	}

	fill := p.fill(change, code)
	if fill == nil {
		return
	}

	p.find(change, code, "added nullable, backfilled and made NOT NULL", true)
	p.add(change, Expand, Short, p.newColumn(table, change.Column, &desired)...)
	p.backfill(change, fill)

	if desired.NotNull != "" {
		p.notNull(change, table, change.Column, desired.NotNull, Contract)
	}
}

// fill is the backfill of an added column from its intent, batched by the
// table's primary key; nil, and the change unsafe, without either.
func (p *planner) fill(change *diff.Change, code string) *Fill {
	expression := p.intents.Backfill[change.Key]
	key := p.primaryKey(change.Table)
	add := statement(change.SQL, safety.AddColumn, change.Table, "")

	switch {
	case expression == "":
		p.unsafe(change, code, "existing rows need @ratel.backfill", add)
	case len(key) == 0:
		p.unsafe(change, code, change.Table+" has no primary key to batch a backfill by", add)
	default:
		return &Fill{
			Table: change.Table, Key: key, Set: change.Column + " = " + expression, Where: change.Column + " IS NULL",
		}
	}

	return nil
}

// backfill adds the batched fill step of a change.
func (p *planner) backfill(change *diff.Change, fill *Fill) {
	sql := "UPDATE " + fill.Table + " SET " + fill.Set + " WHERE " + fill.Where + ";"
	p.add(change, Backfill, Batched, statement(sql, safety.Backfill, fill.Table, ""))
	p.steps[len(p.steps)-1].Fill = fill
}

// primaryKey is the primary key columns of a current table.
func (p *planner) primaryKey(table string) []string {
	for _, object := range p.env.Current.Objects {
		if !strings.HasPrefix(object.Key, "constraint "+table+".") {
			continue
		}

		if parts, ok := uniqueParts(constraintDefinition(object.SQL, constraintName(object.Key))); ok &&
			parts.keyword == "PRIMARY KEY" {
			return parts.columns
		}
	}

	return nil
}

// generated plans a generation expression change: SET EXPRESSION of a
// virtual column and DROP EXPRESSION are metadata only; anything else
// rewrites the table.
func (p *planner) generated(change *diff.Change) {
	table := change.Table
	operation := statement(change.SQL, safety.SetExpression, table, "")

	switch {
	case strings.HasSuffix(change.SQL, " DROP EXPRESSION;"):
		p.short(change, statement(change.SQL, safety.DropExpression, table, ""))
	case strings.HasSuffix(change.After, " VIRTUAL") && strings.Contains(change.SQL, " SET EXPRESSION "):
		p.short(change, operation)
	default:
		operation.Operations[0].Rewrite = true
		p.unsafe(change, "RTL-R01", "a stored generated column is recomputed by a table rewrite", operation)
	}
}

// renameColumn renames through a second column: expand adds it under the
// new name with a trigger keeping both in step, the backfill copies existing
// rows, contract drops the old column once no old code reads it.
func (p *planner) renameColumn(change *diff.Change) {
	table := change.Table
	_, rest, _ := strings.Cut(change.SQL, " RENAME COLUMN ")
	old, _, _ := strings.Cut(rest, " TO ")
	current, _ := p.env.Current.Column(table + "." + old)
	desired, _ := p.env.Desired.Column(strings.TrimPrefix(change.Key, "column "))

	if current.Identity != "" || current.Generated != "" {
		p.unsafe(change, "RTL-C01", "an identity or generated column cannot be copied to its new name",
			statement(change.SQL, safety.RenameColumn, table, ""))

		return
	}

	key := p.primaryKey(table)
	if len(key) == 0 {
		p.unsafe(change, "RTL-C01", table+" has no primary key to batch the copy by",
			statement(change.SQL, safety.RenameColumn, table, ""))

		return
	}

	p.find(change, "RTL-C01", "both names are kept in step until contract", true)
	p.rebuilt[change.Key] = true

	body := "IF TG_OP = 'INSERT' THEN IF NEW." + change.Column + " IS NULL THEN NEW." + change.Column + " := NEW." +
		old + "; ELSE NEW." + old + " := NEW." + change.Column + "; END IF; ELSIF NEW." + change.Column +
		" IS DISTINCT FROM OLD." + change.Column + " THEN NEW." + old + " := NEW." + change.Column +
		"; ELSE NEW." + change.Column + " := NEW." + old + "; END IF;"

	sync := p.sync(table, change.Column, body)
	p.add(change, Expand, Short, append(p.newColumn(table, change.Column, &desired), sync.create...)...)
	p.backfill(change, &Fill{
		Table: table, Key: key, Set: change.Column + " = " + old, Where: change.Column + " IS DISTINCT FROM " + old,
	})

	if desired.NotNull != "" {
		p.notNull(change, table, change.Column, desired.NotNull, Expand)
	}

	p.late = append(p.late, Step{
		Phase: Contract, Class: Short, Change: change.Key, Allow: p.allowed(change),
		Statements: append(sync.drop, statement("ALTER TABLE "+table+" DROP COLUMN "+old+";",
			safety.DropColumn, table, "")),
	})
}

// newColumn adds a nullable copy of a desired column with its default.
func (p *planner) newColumn(table, name string, desired *introspect.Column) []Statement {
	statements := []Statement{statement("ALTER TABLE "+table+" ADD COLUMN "+name+" "+desired.Type+desired.Collation+";",
		safety.AddColumn, table, "")}

	if desired.Default != "" {
		statements = append(statements, statement("ALTER TABLE "+table+" ALTER COLUMN "+name+" SET DEFAULT "+
			desired.Default+";", safety.SetDefault, table, ""))
	}

	return statements
}

// syncTrigger is the statements creating and dropping a sync trigger.
type syncTrigger struct{ create, drop []Statement }

// sync is a BEFORE INSERT OR UPDATE row trigger on table running body,
// named after the column it keeps in step.
func (p *planner) sync(table, column, body string) syncTrigger {
	name := generated(table, "_"+unquote(column)+"_ratel_sync")
	function := schemaOf(table) + "." + name

	return syncTrigger{
		create: []Statement{
			statement("CREATE FUNCTION "+function+"() RETURNS trigger LANGUAGE plpgsql AS $ratel$BEGIN "+body+
				" RETURN NEW; END$ratel$;", safety.CreateRoutine, "", ""),
			statement("CREATE TRIGGER "+name+" BEFORE INSERT OR UPDATE ON "+table+" FOR EACH ROW EXECUTE FUNCTION "+
				function+"();", safety.CreateTrigger, table, ""),
		},
		drop: []Statement{
			statement("DROP TRIGGER "+name+" ON "+table+";", safety.DropTrigger, table, ""),
			statement("DROP FUNCTION "+function+"();", safety.DropRoutine, "", ""),
		},
	}
}
