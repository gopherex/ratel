// Package probe reads what the planner needs from the dev database holding
// the current state: the relations and their partitioning, the tables of
// indexes, and whether column changes rewrite their table or rebuild its
// indexes. A change is probed on an empty temporary copy of its table in a
// transaction that is rolled back, with TimeZone = 'UTC' as every applied
// step runs, so the probe sees the conversion production will.
package probe

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
	"github.com/gopherex/ratel/cmd/ratel/internal/safety"
)

// Source opens transactions on the dev database of the current state.
type Source interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Env builds the planner's view of the current state.
func Env(
	ctx context.Context, source Source, current, desired introspect.Rendering, changes []diff.Change,
	namespaces []string,
) (plan.Env, error) {
	env := plan.Env{
		Safety:  safety.Env{Major: current.Major},
		Current: current, Desired: desired,
		Rewrites: make(map[string]bool), Reindexes: make(map[string]bool),
	}

	transaction, err := source.Begin(ctx)
	if err != nil {
		return plan.Env{}, fmt.Errorf("probe: begin: %w", err)
	}
	// The probe changes nothing it keeps.
	defer func() { _ = transaction.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := transaction.Exec(ctx, `SET LOCAL search_path = ''`); err != nil {
		return plan.Env{}, fmt.Errorf("probe: search_path: %w", err)
	}

	if env.Safety.Relations, err = relationsOf(ctx, transaction, namespaces); err != nil {
		return plan.Env{}, err
	}

	if env.Safety.Indexes, err = indexesOf(ctx, transaction, namespaces); err != nil {
		return plan.Env{}, err
	}

	if env.Dependents, err = dependentsOf(ctx, transaction, namespaces); err != nil {
		return plan.Env{}, err
	}

	for index := range changes {
		if err := probe(ctx, transaction, &changes[index], &env); err != nil {
			return plan.Env{}, err
		}
	}

	return env, nil
}

// relations are the tables, views and sequences of the namespaces with
// their partitioning.
const relationsQuery = `
SELECT format('%I.%I', n.nspname, c.relname), c.relkind = 'p',
       COALESCE(pt.partdefid <> 0, false),
       COALESCE((SELECT array_agg(format('%I', a.attname) ORDER BY k.ordinality)
                 FROM unnest(pt.partattrs) WITH ORDINALITY k(attnum, ordinality)
                 JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum), '{}'),
       COALESCE((SELECT array_agg(format('%I.%I', pn.nspname, pc.relname) ORDER BY pn.nspname, pc.relname)
                 FROM pg_catalog.pg_inherits i
                 JOIN pg_catalog.pg_class pc ON pc.oid = i.inhrelid
                 JOIN pg_catalog.pg_namespace pn ON pn.oid = pc.relnamespace
                 WHERE i.inhparent = c.oid), '{}')
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_catalog.pg_partitioned_table pt ON pt.partrelid = c.oid
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S') AND n.nspname = ANY($1)`

func relationsOf(ctx context.Context, transaction pgx.Tx, namespaces []string) (map[string]safety.Relation, error) {
	rows, err := transaction.Query(ctx, relationsQuery, namespaces)
	if err != nil {
		return nil, fmt.Errorf("probe: relations: %w", err)
	}

	defer rows.Close()

	result := make(map[string]safety.Relation)

	for rows.Next() {
		var (
			name     string
			relation safety.Relation
		)

		err := rows.Scan(&name, &relation.Partitioned, &relation.DefaultPartition, &relation.PartitionKey,
			&relation.Partitions)
		if err != nil {
			return nil, fmt.Errorf("probe: relations: %w", err)
		}

		result[name] = relation
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("probe: relations: %w", err)
	}

	return result, nil
}

const indexesQuery = `
SELECT format('%I.%I', n.nspname, ic.relname), format('%I.%I', tn.nspname, tc.relname)
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
JOIN pg_catalog.pg_namespace n ON n.oid = ic.relnamespace
JOIN pg_catalog.pg_class tc ON tc.oid = i.indrelid
JOIN pg_catalog.pg_namespace tn ON tn.oid = tc.relnamespace
WHERE n.nspname = ANY($1)`

func indexesOf(ctx context.Context, transaction pgx.Tx, namespaces []string) (map[string]string, error) {
	rows, err := transaction.Query(ctx, indexesQuery, namespaces)
	if err != nil {
		return nil, fmt.Errorf("probe: indexes: %w", err)
	}

	defer rows.Close()

	result := make(map[string]string)

	for rows.Next() {
		var index, table string
		if err := rows.Scan(&index, &table); err != nil {
			return nil, fmt.Errorf("probe: indexes: %w", err)
		}

		result[index] = table
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("probe: indexes: %w", err)
	}

	return result, nil
}

// probed is whether a change kind may rewrite its table or rebuild indexes.
func probed(kind diff.Kind) bool {
	switch kind {
	case diff.AddColumn, diff.AlterColumnType, diff.AlterCollation, diff.AlterGenerated:
		return true
	default:
		return false
	}
}

// probeTable is the temporary copy a change runs against.
const probeTable = "pg_temp.ratel_probe"

// files are the relfilenodes of the probe table and its indexes.
const files = `
SELECT c.relfilenode, COALESCE(array_agg(ic.relfilenode ORDER BY ic.relname) FILTER (WHERE ic.oid IS NOT NULL), '{}')
FROM pg_catalog.pg_class c
LEFT JOIN pg_catalog.pg_index i ON i.indrelid = c.oid
LEFT JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
WHERE c.oid = 'pg_temp.ratel_probe'::regclass
GROUP BY c.relfilenode`

// probe runs a column change on an empty copy of its table under a
// savepoint and records whether the table or its indexes got new files.
func probe(ctx context.Context, transaction pgx.Tx, change *diff.Change, env *plan.Env) error {
	prefix := "ALTER TABLE " + change.Table + " "
	if !probed(change.Kind) || !env.Safety.Exists(change.Table) || !strings.HasPrefix(change.SQL, prefix) {
		return nil
	}

	savepoint, err := transaction.Begin(ctx)
	if err != nil {
		return fmt.Errorf("probe: savepoint: %w", err)
	}

	defer func() { _ = savepoint.Rollback(context.WithoutCancel(ctx)) }()

	setup := []string{
		`SET LOCAL TimeZone = 'UTC'`,
		"CREATE TEMP TABLE ratel_probe (LIKE " + change.Table + " INCLUDING ALL)",
	}
	for _, sql := range setup {
		if _, err := savepoint.Exec(ctx, sql); err != nil {
			return fmt.Errorf("probe: %s: %w", change.Key, err)
		}
	}

	var (
		table   uint32
		indexes []uint32
	)

	if err := savepoint.QueryRow(ctx, files).Scan(&table, &indexes); err != nil {
		return fmt.Errorf("probe: %s: %w", change.Key, err)
	}

	probing := strings.ReplaceAll(change.SQL, prefix, "ALTER TABLE "+probeTable+" ")
	if _, err := savepoint.Exec(ctx, probing); err != nil {
		return fmt.Errorf("probe: %s: %w", change.Key, err)
	}

	var (
		tableAfter   uint32
		indexesAfter []uint32
	)

	if err := savepoint.QueryRow(ctx, files).Scan(&tableAfter, &indexesAfter); err != nil {
		return fmt.Errorf("probe: %s: %w", change.Key, err)
	}

	env.Rewrites[change.Key] = table != tableAfter
	env.Reindexes[change.Key] = !slices.Equal(indexes, indexesAfter)

	return nil
}
