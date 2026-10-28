package plan

import (
	"strings"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// index plans index changes.
func (p *planner) index(change *diff.Change) bool {
	switch change.Kind {
	case diff.CreateIndex:
		p.createIndex(change, lastPart(nameOf(change.Key)), change.SQL)
	case diff.DropIndex:
		p.dropIndex(change)
	case diff.RenameIndex:
		p.short(change, statement(change.SQL, safety.RenameIndex, nameOf(change.Key), ""))
	default:
		return false
	}

	return true
}

// createIndex builds an index named name from its rendered CREATE INDEX:
// concurrently on a plain table, per partition and attached on a
// partitioned one, as written on a table created in the same plan.
func (p *planner) createIndex(change *diff.Change, name, sql string) {
	table := change.Table
	sql = renamedIndex(sql, lastPart(nameOf(change.Key)), name)

	switch {
	case !p.env.Safety.Exists(table):
		p.short(change, statement(sql, safety.CreateIndex, table, ""))
	case p.env.Safety.Relations[table].Partitioned:
		p.find(change, "RTL-L02", "built per partition concurrently and attached", true)
		p.partitionedIndex(change, table, schemaOf(nameOf(change.Key))+"."+name, sql)
	default:
		p.find(change, "RTL-L02", "built concurrently", true)
		p.add(change, Expand, Long, statement(strings.Replace(sql, " INDEX ", " INDEX CONCURRENTLY ", 1),
			safety.CreateIndexConcurrently, table, ""))
	}
}

// partitionedIndex creates the invalid parent index on the partitioned
// table only, then builds each partition's index concurrently, recursing
// into sub-partitioned partitions, and attaches it. The parent index becomes
// valid when every partition's index is attached.
func (p *planner) partitionedIndex(change *diff.Change, table, index, sql string) {
	sql = strings.Replace(sql, " ON ONLY "+table+" ", " ON "+table+" ", 1)
	only := strings.Replace(sql, " ON "+table+" ", " ON ONLY "+table+" ", 1)
	p.add(change, Expand, Short, statement(only, safety.CreateIndexOnly, table, ""))

	for _, partition := range p.env.Safety.Relations[table].Partitions {
		child := schemaOf(partition) + "." + generated(partition, "_"+unquote(lastPart(index)))
		partitionSQL := strings.Replace(renamedIndex(sql, lastPart(index), lastPart(child)),
			" ON "+table+" ", " ON "+partition+" ", 1)

		if p.env.Safety.Relations[partition].Partitioned {
			p.partitionedIndex(change, partition, child, partitionSQL)
		} else {
			p.add(change, Expand, Long, statement(strings.Replace(partitionSQL, " INDEX ", " INDEX CONCURRENTLY ", 1),
				safety.CreateIndexConcurrently, partition, ""))
		}

		p.add(change, Expand, Short, statement("ALTER INDEX "+index+" ATTACH PARTITION "+child+";",
			safety.AttachIndex, index, ""))
	}
}

// dropIndex drops concurrently, except on a partitioned table, where a drop
// is metadata only and concurrent drop is not supported.
func (p *planner) dropIndex(change *diff.Change) {
	index := nameOf(change.Key)

	if p.env.Safety.Relations[change.Table].Partitioned {
		p.short(change, statement(change.SQL, safety.DropIndex, change.Table, ""))

		return
	}

	p.find(change, "RTL-L03", "dropped concurrently", true)
	p.add(change, Contract, Long, statement("DROP INDEX CONCURRENTLY "+index+";",
		safety.DropIndexConcurrently, change.Table, ""))
}

// replaceIndex changes an index definition: the new index is built under a
// temporary name, the old one dropped, and the new one renamed.
func (p *planner) replaceIndex(drop, create *diff.Change) {
	index := nameOf(create.Key)
	temporary := generated(index, "_ratel_new")

	p.createIndex(create, temporary, create.SQL)
	p.dropIndex(drop)
	p.add(create, Contract, Short, statement("ALTER INDEX "+schemaOf(index)+"."+temporary+" RENAME TO "+
		lastPart(index)+";", safety.RenameIndex, schemaOf(index)+"."+temporary, ""))
}

// renamedIndex is a rendered CREATE INDEX under another name.
func renamedIndex(sql, from, to string) string {
	return strings.Replace(sql, " INDEX "+from+" ON ", " INDEX "+to+" ON ", 1)
}
