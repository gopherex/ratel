package plan

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/pgname"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// attach plans a table becoming a partition. A new partition is created in
// place; an existing table first gets a validated check proving every row is
// inside the bound, so ATTACH does not scan it under its exclusive lock. A
// default partition is still scanned for rows the new bound takes.
func (p *planner) attach(change *diff.Change) {
	table := nameOf(change.Key)
	parent := change.Table
	scansDefault := p.env.Safety.Relations[parent].DefaultPartition

	if strings.HasPrefix(change.SQL, "CREATE TABLE ") {
		create := Statement{SQL: change.SQL, Operations: []safety.Operation{
			{Kind: safety.CreateTable, Relation: table}, {Kind: safety.CreatePartition, Relation: parent, Other: table},
		}}

		if scansDefault {
			p.unsafe(change, "RTL-L07", "the default partition of "+parent+" is scanned under an exclusive lock", create)

			return
		}

		p.short(change, create)

		return
	}

	attach := statement(change.SQL, safety.AttachPartitionChecked, parent, table)
	_, bound, _ := strings.Cut(change.SQL, " ATTACH PARTITION "+table+" ")
	check, expressible := boundCheck(p.env.Safety.Relations[parent].PartitionKey, strings.TrimSuffix(bound, ";"))

	switch {
	case scansDefault || !expressible:
		attach.Operations[0].Kind = safety.AttachPartition
		p.unsafe(change, "RTL-L07", "ATTACH scans "+table+" or the default partition under an exclusive lock", attach)
	default:
		name := generated(table, "_ratel_bound")

		p.find(change, "RTL-L07", "a validated bound check lets ATTACH skip the scan", true)
		p.add(change, Expand, Short, statement("ALTER TABLE "+table+" ADD CONSTRAINT "+name+" CHECK ("+check+") NOT VALID;",
			safety.AddCheckNotValid, table, ""))
		p.add(change, Expand, Long, statement("ALTER TABLE "+table+" VALIDATE CONSTRAINT "+name+";",
			safety.ValidateConstraint, table, ""))
		p.add(change, Expand, Short, attach,
			statement("ALTER TABLE "+table+" DROP CONSTRAINT "+name+";", safety.DropConstraint, table, ""))
	}
}

// detach plans a partition leaving its parent, concurrently unless the
// parent has a default partition. A partition that stays one, under another
// bound or parent, moves in expand; a partition becoming a plain table or
// dropped leaves in contract.
func (p *planner) detach(change *diff.Change) {
	parent := change.Table
	table := nameOf(change.Key)
	phase := Contract

	if strings.Contains(change.After, " PARTITION OF ") {
		phase = Expand
	} else {
		// What changes on the table now plain follows its detach.
		p.floor[table] = Contract
	}

	if p.env.Safety.Relations[parent].DefaultPartition {
		p.add(change, phase, Short, statement("ALTER TABLE "+parent+" DETACH PARTITION "+table+";",
			safety.DetachPartition, parent, table))
	} else {
		p.add(change, phase, Long, statement("ALTER TABLE "+parent+" DETACH PARTITION "+table+" CONCURRENTLY;",
			safety.DetachConcurrently, parent, table))
		// A concurrent detach keeps the partition bound as a check
		// constraint, named as PostgreSQL names a check on the key.
		p.add(change, phase, Short, statement("ALTER TABLE "+table+" DROP CONSTRAINT IF EXISTS "+
			p.boundCheckName(parent, table)+";", safety.DropConstraint, table, ""))
	}

	if strings.Contains(change.SQL, "DROP TABLE ") {
		p.add(change, Contract, Short, statement("DROP TABLE "+table+";", safety.DropTable, table, ""))
	}
}

// boundCheckName is the name DETACH CONCURRENTLY gives the check it adds:
// the table and its single key column, or the table alone, then "check".
func (p *planner) boundCheckName(parent, table string) string {
	key := p.env.Safety.Relations[parent].PartitionKey

	column := ""
	if len(key) == 1 {
		column = unquote(key[0])
	}

	used := func(name string) bool {
		_, exists := p.env.Current.Statement("constraint " + table + "." + quote(name))

		return exists
	}

	return quote(pgname.Choose(unquote(lastPart(table)), column, "check", used))
}

// boundCheck is a check expression equal to a single-column range or list
// partition bound, including the NOT NULL the bound implies; false for
// bounds it cannot express.
func boundCheck(key []string, bound string) (string, bool) {
	if len(key) != 1 {
		return "", false
	}

	column := key[0]

	if values, ok := strings.CutPrefix(bound, "FOR VALUES IN ("); ok {
		values = strings.TrimSuffix(values, ")")
		if strings.Contains(values, "NULL") {
			return "", false
		}

		return column + " IS NOT NULL AND " + column + " IN (" + values + ")", true
	}

	rest, ranged := strings.CutPrefix(bound, "FOR VALUES FROM (")
	if !ranged {
		return "", false
	}

	lower, upper, ranged := strings.Cut(strings.TrimSuffix(rest, ")"), ") TO (")
	if !ranged || strings.Contains(lower+upper, ",") {
		return "", false
	}

	parts := []string{column + " IS NOT NULL"}
	if lower != "MINVALUE" {
		parts = append(parts, column+" >= "+lower)
	}

	if upper != "MAXVALUE" {
		parts = append(parts, column+" < "+upper)
	}

	return strings.Join(parts, " AND "), true
}
