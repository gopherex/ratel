// Package rateltest provides PostgreSQL fixtures and executor test seams for ratel.
//
// NewServer creates a suite-owned testcontainers PostgreSQL and a migrated
// template; Server.Postgres gives each parallel subtest its own database and pool.
// Postgres is the private-server convenience form. RollbackTx provides cheaper
// transaction isolation when the application does not manage transactions.
//
// Configure pgxpool AfterConnect through WithPoolConfig to call a generated
// RegisterTypes, when a package has one. Recorder traces query, batch and COPY
// attempts. FailWith injects selected SQLSTATE failures, while NewExecutor
// provides a pgxmock-backed scripted executor with cleanup assertions.
//
// Test application use cases through application-owned ports. Test their ratel
// implementations against real PostgreSQL; the scripted executor is intended
// for runtime-level tests, not for duplicating generated SQL in application tests.
package rateltest
