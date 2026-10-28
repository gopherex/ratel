package ratel

import "slices"

// ErrStaleVersion means an update with ExpectVersion matched no row: another
// writer changed the version, or the row is gone.
const ErrStaleVersion refusal = "ratel: stale version"

// Keyed is implemented only by generated descriptors of tables with a primary
// key; Save and DeleteRow take it, so a table without one cannot address a row
// by identity.
type Keyed[M, I any] interface {
	Writable[M, I]
	ratelKeyed() KeyedTable[M, I]
}

// KeyedTable is the write metadata of a table with a primary key.
type KeyedTable[M, I any] struct {
	WriteTable[M, I]
}

// NewKeyedTable marks generated write metadata as addressable by primary key.
func NewKeyedTable[M, I any](table WriteTable[M, I]) KeyedTable[M, I] {
	return KeyedTable[M, I]{WriteTable: table}
}

// InSchema preserves keyed metadata for another schema.
func (t KeyedTable[M, I]) InSchema(schema string) KeyedTable[M, I] {
	t.WriteTable = t.WriteTable.InSchema(schema)

	return t
}

func (t KeyedTable[M, I]) ratelKeyed() KeyedTable[M, I] { return t }

// Save updates the model's row by primary key: every insertable column that is
// not part of the key takes the model's value. No matching row is
// ErrNotFound; with ExpectVersion it is ErrStaleVersion.
func (db *DB) Save[M, I any](table Keyed[M, I], model M) Write[M] {
	keyed := table.ratelKeyed()
	query := db.Update(keyed).byKey(model)

	if keyed.values == nil {
		query.invalid = true

		return query
	}

	values := keyed.values(model)

	for index, column := range keyed.columns {
		insertable := slices.ContainsFunc(keyed.fields, func(field InsertField[I]) bool { return field.Name == column })
		if insertable && !slices.Contains(keyed.keys, column) && index < len(values) {
			query.assignments = append(query.assignments, Assignment[M]{name: column, value: values[index]})
		}
	}

	return query
}

// DeleteRow deletes the model's row by primary key; no matching row is
// ErrNotFound.
func (db *DB) DeleteRow[M, I any](table Keyed[M, I], model M) Write[M] {
	return db.Delete(table.ratelKeyed()).byKey(model)
}

// byKey addresses one row by the model's primary key.
func (q Write[M]) byKey(model M) Write[M] {
	if len(q.table.keys) == 0 || q.table.keyValues == nil {
		q.invalid = true

		return q
	}

	values := q.table.keyValues(model)
	if len(values) != len(q.table.keys) {
		q.invalid = true

		return q
	}

	for index, key := range q.table.keys {
		q = q.Where(Cond[M]{expr: comparison(key, " = ", values[index])})
	}

	q.missing = ErrNotFound

	return q
}

// ExpectVersion adds an optimistic-locking predicate on a version column; an
// update that then matches no row returns ErrStaleVersion.
func (q Write[M]) ExpectVersion[V any](column Col[M, V], version V) Write[M] {
	q = q.Where(column.Eq(version))
	q.missing = ErrStaleVersion

	return q
}

func comparison(column, operator string, value any) func(*sqlWriter) {
	return func(writer *sqlWriter) {
		writer.column(column)
		writer.WriteString(operator)
		writer.param(value)
	}
}
