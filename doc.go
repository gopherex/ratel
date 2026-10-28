// Package ratel is the runtime library under every ratel-generated
// data-access package.
//
// ratel generates thin per-schema packages (models, table descriptors with
// typed columns and relations, reflection-free scanners, static queries) on
// top of this package, which holds the generic core built on Go 1.27 generic
// methods: the query builder, relation loading, the executor port and the
// value types for nullable and defaulted columns (Null and Opt).
//
// The package owns no connections and no transactions: it runs over a
// caller-provided Executor (a pool, a connection, a pgx.Tx or a context-routed
// executor such as github.com/gopherex/pgtx).
package ratel
