// Package rateltest provides test helpers for applications built on ratel.
//
// Planned helpers: a shared testcontainers PostgreSQL with per-test databases
// created from a template, a rollback transaction for code that opens none of
// its own, an SQL recorder for query-count assertions and failure-injection
// executors. The package is a placeholder until the runtime lands.
package rateltest
