// Package ratel is the runtime library under every ratel-generated
// data-access package.
//
// ratel generates thin per-schema packages (models, table descriptors with
// typed columns and relations, reflection-free scanners, static queries) on
// top of this package, which holds the generic core built on Go 1.27 generic
// methods: the query builder, relation loading, the executor port and the
// value types for nullable and defaulted columns (Null and Opt).
//
// Every statement is a value built from a descriptor or a generated query,
// without a database: Table.Select, WriteTable.Insert, Upsert, Update and
// Delete, KeyedTable.Save and DeleteRow, Query.Select and From. Build renders
// it as SQL and arguments. DB.Run binds it to a database, and the type of the
// value decides the verbs of the runner it returns: row verbs for a SELECT, a
// static row query or a write with Returning, Exec for a write without it.
//
// The package owns no connections and no transactions: it runs over a
// caller-provided Executor (a pool, a connection, a pgx.Tx or a context-routed
// executor such as github.com/gopherex/pgtx).
package ratel
