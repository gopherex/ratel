package ratel

import "strconv"

// LinkWrite is one statement on a many-to-many relation's join table, built
// by Attach, Detach or Sync; db.Run gives it Exec.
type LinkWrite struct {
	statement stmt
	err       error
	target    writeTarget
}

// Build renders the statement and its arguments without executing it.
func (w LinkWrite) Build() (string, []any, error) {
	if err := w.failure(); err != nil {
		return "", nil, err
	}

	return w.statement.SQL, append([]any(nil), w.statement.Args...), nil
}

// failure is the build error; the zero LinkWrite has no statement to send.
func (w LinkWrite) failure() error {
	if w.err == nil && w.statement.SQL == "" {
		return ErrInvalidQuery
	}

	return w.err
}

func (w LinkWrite) execPlan() execPlan {
	return execPlan{plan: plan{head: w.statement, err: w.failure()}, target: w.target}
}

func (w LinkWrite) bind(db *DB) ExecRunner { return ExecRunner{db: db, plan: w.execPlan()} }

// Attach links the children to the parent: one INSERT into the join table;
// links that exist are skipped (ON CONFLICT DO NOTHING). Exec returns the
// number of new links.
func (r Rel[M, R]) Attach(parent M, children ...R) LinkWrite {
	return r.link(parent, children, false, func(writer *sqlWriter, link linkSQL) {
		link.insert(writer)
	})
}

// Detach removes the links between the parent and the children: one DELETE
// from the join table. Exec returns the number of removed links.
func (r Rel[M, R]) Detach(parent M, children ...R) LinkWrite {
	return r.link(parent, children, false, func(writer *sqlWriter, link linkSQL) {
		link.delete(writer, " IN ")
	})
}

// Sync makes the parent's links exactly the children: one statement deletes
// the other links and inserts the missing ones, atomic on any executor. Exec
// returns the number of new links; without children it removes every link
// of the parent and returns their number.
func (r Rel[M, R]) Sync(parent M, children ...R) LinkWrite {
	return r.link(parent, children, true, func(writer *sqlWriter, link linkSQL) {
		if len(link.children) == 0 {
			link.delete(writer, "")

			return
		}

		writer.WriteString(`WITH "removed" AS (`)
		link.delete(writer, " NOT IN ")
		writer.WriteString(") ")
		link.insert(writer)
	})
}

// link validates a many-to-many write and renders it.
func (r Rel[M, R]) link(parent M, children []R, empty bool, render func(*sqlWriter, linkSQL)) LinkWrite {
	if !r.validTarget() || !r.validKeys() || len(r.through) == 0 || !r.validThrough() ||
		(len(children) == 0 && !empty) {
		return LinkWrite{err: ErrInvalidQuery}
	}

	table := r.query.source.table

	link := linkSQL{through: r.through, targetColumns: r.targetColumns, children: make([][]any, len(children))}
	for index := range children {
		link.children[index] = table.keyValues(children[index])
		if len(link.children[index]) != len(r.targetColumns) {
			return LinkWrite{err: ErrInvalidQuery}
		}
	}

	writer := newSQLWriter()

	// Each parent key is bound once as a one-element array of its projection
	// and referenced from every row, so pgx sends it with the key's type.
	for _, key := range r.keys {
		writer.args = append(writer.args, key.Values([]M{parent}))
		reference := "($" + strconv.Itoa(len(writer.args))

		if len(key.Base) > 0 {
			reference += "::" + key.Base.Sanitize() + "[]"
		}

		link.parentColumns = append(link.parentColumns, key.Column)
		link.parentValues = append(link.parentValues, reference+"::"+key.Type.Sanitize()+"[])[1]")
	}

	render(writer, link)

	if writer.invalid {
		return LinkWrite{err: ErrInvalidQuery}
	}

	return LinkWrite{statement: writer.result(), target: writeTarget{schema: r.through[0], name: r.through[1]}}
}

// linkSQL is a join-table write: the parent's key columns and value
// expressions, and each child's target-key values.
type linkSQL struct {
	through       []string
	parentColumns []string
	parentValues  []string
	targetColumns []string
	children      [][]any
}

func (l linkSQL) insert(writer *sqlWriter) {
	writer.WriteString("INSERT INTO ")
	writer.qualifiedColumn(l.through[0], l.through[1])
	writer.WriteString(" (")
	l.columns(writer, l.parentColumns)
	writer.WriteString(", ")
	l.columns(writer, l.targetColumns)
	writer.WriteString(") VALUES ")

	for row, values := range l.children {
		if row > 0 {
			writer.WriteString(", ")
		}

		writer.WriteByte('(')

		for index, value := range l.parentValues {
			if index > 0 {
				writer.WriteString(", ")
			}

			writer.WriteString(value)
		}

		for _, value := range values {
			writer.WriteString(", ")
			writer.param(value)
		}

		writer.WriteByte(')')
	}

	writer.WriteString(" ON CONFLICT DO NOTHING")
}

// delete removes the parent's links; operator " IN " or " NOT IN " limits it
// to the children, "" removes all of them.
func (l linkSQL) delete(writer *sqlWriter, operator string) {
	writer.WriteString("DELETE FROM ")
	writer.qualifiedColumn(l.through[0], l.through[1])
	writer.WriteString(" WHERE (")
	l.columns(writer, l.parentColumns)
	writer.WriteString(") = (")

	for index, value := range l.parentValues {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.WriteString(value)
	}

	writer.WriteByte(')')

	if operator == "" {
		return
	}

	writer.WriteString(" AND (")
	l.columns(writer, l.targetColumns)
	writer.WriteString(")")
	writer.WriteString(operator)
	writer.WriteByte('(')

	for row, values := range l.children {
		if row > 0 {
			writer.WriteString(", ")
		}

		writer.WriteByte('(')

		for index, value := range values {
			if index > 0 {
				writer.WriteString(", ")
			}

			writer.param(value)
		}

		writer.WriteByte(')')
	}

	writer.WriteByte(')')
}

func (linkSQL) columns(writer *sqlWriter, names []string) {
	for index, name := range names {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.identifier(name)
	}
}
