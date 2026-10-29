package plan

import (
	"strings"

	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// postgres18 is the first major version with NOT NULL ... NOT VALID and
// NOT VALID foreign keys on partitioned tables.
const postgres18 = 18

// constraint plans table constraints. A constraint on a table created in the
// same plan is added as written: nothing reads the table yet.
func (p *planner) constraint(change *diff.Change) bool {
	switch change.Kind {
	case diff.AddCheck, diff.AddForeignKey, diff.AddUnique, diff.AddPrimaryKey, diff.AddExclusion:
	case diff.DropConstraint:
		p.short(change, statement(change.SQL, safety.DropConstraint, change.Table, ""))

		return true
	case diff.RenameConstraint:
		p.short(change, statement(change.SQL, safety.RenameConstraint, change.Table, ""))

		return true
	default:
		return false
	}

	if !p.env.Safety.Exists(change.Table) {
		p.short(change, statement(change.SQL, addKind(change.Kind), change.Table, referenced(change.SQL)))

		return true
	}

	p.addConstraint(change, constraintName(change.Key), "")

	return true
}

// addConstraint adds a constraint to an existing table under name, keeping
// it off the table until validated. A non-empty replaces is the constraint
// the new one replaces, which is dropped and its name taken in contract.
func (p *planner) addConstraint(change *diff.Change, name, replaces string) {
	table := change.Table
	definition := constraintDefinition(change.SQL, constraintName(change.Key))
	add := "ALTER TABLE " + table + " ADD CONSTRAINT " + name + " " + definition

	switch change.Kind {
	case diff.AddCheck, diff.AddForeignKey:
		other := referenced(change.SQL)
		if change.Kind == diff.AddForeignKey && p.env.Safety.Relations[table].Partitioned &&
			p.env.Safety.Major < postgres18 {
			p.unsafe(change, "RTL-L04", "PostgreSQL before 18 rejects NOT VALID foreign keys on partitioned tables",
				statement(add+";", safety.AddForeignKey, table, other))

			return
		}

		kind, validate := safety.AddCheckNotValid, safety.ValidateConstraint
		if change.Kind == diff.AddForeignKey {
			kind, validate = safety.AddForeignKeyNotValid, safety.ValidateForeignKey
		}

		p.find(change, "RTL-L04", "added NOT VALID and validated without a blocking lock", true)
		p.add(change, migrate.PhaseExpand, migrate.ClassShort, statement(add+" NOT VALID;", kind, table, other))
		p.add(change, migrate.PhaseContract, migrate.ClassLong,
			statement("ALTER TABLE "+table+" VALIDATE CONSTRAINT "+name+";",
				validate, table, other))
	case diff.AddUnique, diff.AddPrimaryKey:
		p.unique(change, name, definition)
	default:
		p.unsafe(change, "RTL-L09", "an exclusion constraint builds its index under an exclusive lock",
			statement(add+";", safety.AddExclusion, table, ""))

		return
	}

	if replaces != "" {
		p.add(change, migrate.PhaseContract, migrate.ClassShort,
			statement("ALTER TABLE "+table+" DROP CONSTRAINT "+replaces+";", safety.DropConstraint, table, ""),
			statement("ALTER TABLE "+table+" RENAME CONSTRAINT "+name+" TO "+replaces+";",
				safety.RenameConstraint, table, ""))
	}
}

// unique builds the index of a unique or primary key constraint
// concurrently and attaches it. A primary key first gets NOT NULL on its
// columns; ADD ... USING INDEX would otherwise scan under its lock.
func (p *planner) unique(change *diff.Change, name, definition string) {
	table := change.Table

	if p.env.Safety.Relations[table].Partitioned {
		p.unsafe(change, "RTL-L05", "USING INDEX is not supported on partitioned tables",
			statement(change.SQL, safety.AddUnique, table, ""))

		return
	}

	parts, ok := uniqueParts(definition)
	if !ok {
		p.unsafe(change, "RTL-L05", "the constraint's index cannot be built concurrently",
			statement(change.SQL, safety.AddUnique, table, ""))

		return
	}

	if change.Kind == diff.AddPrimaryKey {
		for _, column := range parts.columns {
			p.requireNotNull(change, table, column)
		}
	}

	p.find(change, "RTL-L05", "index built concurrently and attached with USING INDEX", true)
	p.add(change, migrate.PhaseExpand, migrate.ClassLong,
		statement("CREATE UNIQUE INDEX CONCURRENTLY "+name+" ON "+table+" USING btree ("+
			strings.Join(parts.columns, ", ")+")"+parts.index+";", safety.CreateIndexConcurrently, table, ""))
	p.add(change, migrate.PhaseContract, migrate.ClassShort,
		statement("ALTER TABLE "+table+" ADD CONSTRAINT "+name+" "+parts.keyword+
			" USING INDEX "+name+parts.deferrable+";", safety.AddConstraintIndex, table, ""))
}

// requireNotNull plans NOT NULL on a column that lacks it.
func (p *planner) requireNotNull(change *diff.Change, table, column string) {
	current, exists := p.env.Current.Column(table + "." + column)
	if exists && current.NotNull != "" {
		return
	}

	clause := " NOT NULL"
	if desired, ok := p.env.Desired.Column(table + "." + column); ok && desired.NotNull != "" {
		clause = desired.NotNull
	}

	p.notNull(change, table, column, clause, migrate.PhaseExpand)
}

// notNull sets NOT NULL on an existing column without scanning under the
// table's exclusive lock: on 18 as a NOT VALID not-null constraint validated
// later, before 18 through a validated IS NOT NULL check that SET NOT NULL
// trusts. The NOT VALID part goes to phase first: contract when old code may
// still insert NULL.
func (p *planner) notNull(change *diff.Change, table, column, clause string, first migrate.Phase) {
	if p.notNulled[table+"."+column] {
		return
	}

	p.notNulled[table+"."+column] = true
	alter := "ALTER TABLE " + table + " "

	p.find(change, "RTL-L06", "NOT NULL proved by a validated constraint instead of a scan", true)

	if p.env.Safety.Major >= postgres18 {
		p.add(change, first, migrate.ClassShort,
			statement(alter+"ADD CONSTRAINT "+notNullName(table, column, clause)+" NOT NULL "+
				column+" NOT VALID;", safety.NotNullNotValid, table, ""))
		p.add(change, migrate.PhaseContract, migrate.ClassLong,
			statement(alter+"VALIDATE CONSTRAINT "+notNullName(table, column, clause)+";",
				safety.ValidateConstraint, table, ""))

		return
	}

	check := generated(table, "_"+unquote(column)+"_ratel_not_null")

	p.add(change, first, migrate.ClassShort,
		statement(alter+"ADD CONSTRAINT "+check+" CHECK ("+column+" IS NOT NULL) NOT VALID;",
			safety.AddCheckNotValid, table, ""))
	p.add(change, migrate.PhaseContract, migrate.ClassLong,
		statement(alter+"VALIDATE CONSTRAINT "+check+";", safety.ValidateConstraint, table, ""))
	p.add(change, migrate.PhaseContract, migrate.ClassShort,
		statement(alter+"ALTER COLUMN "+column+" SET NOT NULL;", safety.SetNotNullChecked, table, ""),
		statement(alter+"DROP CONSTRAINT "+check+";", safety.DropConstraint, table, ""))
}

// notNullName is the not-null constraint name of a " CONSTRAINT x NOT NULL"
// clause, or the name PostgreSQL 18 gives an unnamed one.
func notNullName(table, column, clause string) string {
	if name, ok := strings.CutPrefix(clause, " CONSTRAINT "); ok {
		return strings.TrimSuffix(name, " NOT NULL")
	}

	return generated(table, "_"+unquote(column)+"_not_null")
}

// addKind is the operation adding a constraint as written.
func addKind(kind diff.Kind) safety.Kind {
	switch kind {
	case diff.AddForeignKey:
		return safety.AddForeignKey
	case diff.AddUnique, diff.AddPrimaryKey:
		return safety.AddUnique
	case diff.AddExclusion:
		return safety.AddExclusion
	default:
		return safety.AddCheck
	}
}

// constraintName is the constraint name of a "constraint s.t.c" key.
func constraintName(key string) string { return lastPart(nameOf(key)) }

// constraintDefinition is the definition after the name of a rendered
// ADD CONSTRAINT statement.
func constraintDefinition(sql, name string) string {
	_, rest, _ := strings.Cut(sql, " ADD CONSTRAINT "+name+" ")

	return strings.TrimSuffix(rest, ";")
}

// referenced is the table a FOREIGN KEY definition references.
func referenced(sql string) string {
	_, rest, ok := strings.Cut(sql, " REFERENCES ")
	if !ok {
		return ""
	}

	table, _, _ := strings.Cut(rest, "(")

	return strings.TrimSpace(table)
}

// unique is a UNIQUE or PRIMARY KEY definition split into the parts of its
// index and its constraint.
type uniqueDefinition struct {
	keyword    string   // "UNIQUE" or "PRIMARY KEY"
	columns    []string // rendered column names
	index      string   // INCLUDE, NULLS NOT DISTINCT and WITH clauses of the index
	deferrable string   // " DEFERRABLE ..." of the constraint
}

// uniqueParts splits pg_get_constraintdef output of a unique or primary key
// constraint; false when the index cannot back the constraint through USING
// INDEX (a tablespace is not carried over).
func uniqueParts(definition string) (uniqueDefinition, bool) {
	var parts uniqueDefinition

	switch {
	case strings.HasPrefix(definition, "PRIMARY KEY "):
		parts.keyword = "PRIMARY KEY"
	case strings.HasPrefix(definition, "UNIQUE "):
		parts.keyword = "UNIQUE"
	default:
		return parts, false
	}

	rest := strings.TrimPrefix(definition, parts.keyword+" ")

	nulls := ""
	if after, ok := strings.CutPrefix(rest, "NULLS NOT DISTINCT "); ok {
		rest, nulls = after, " NULLS NOT DISTINCT"
	}

	columns, rest, ok := strings.Cut(strings.TrimPrefix(rest, "("), ")")
	if !ok || strings.Contains(rest, " USING INDEX TABLESPACE ") {
		return parts, false
	}

	parts.columns = strings.Split(columns, ", ")

	if deferrable := strings.Index(rest, " DEFERRABLE"); deferrable >= 0 {
		rest, parts.deferrable = rest[:deferrable], rest[deferrable:]
	}

	// CREATE INDEX takes INCLUDE, then NULLS NOT DISTINCT, then WITH.
	include, with, _ := strings.Cut(rest, " WITH (")
	if with != "" {
		with = " WITH (" + with
	}

	parts.index = include + nulls + with

	return parts, true
}
