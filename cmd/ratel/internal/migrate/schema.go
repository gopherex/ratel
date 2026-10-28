package migrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	pg "github.com/pganalyze/pg_query_go/v6"

	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// ApplySchema creates the objects of schema files on a database. The files,
// like the catalog built from them, may be in any order: foreign keys are
// added after every table exists, so cycles apply, and each other statement
// is retried until the ones it depends on exist. Everything runs in one
// transaction with check_function_bodies off, as pg_dump restores.
func ApplySchema(ctx context.Context, database *pgx.Conn, parser parse.Parser, files []catalog.File) error {
	statements, err := schemaStatements(parser, files)
	if err != nil {
		return err
	}

	transaction, err := database.Begin(ctx)
	if err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	defer func() { _ = transaction.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := transaction.Exec(ctx, `SET LOCAL check_function_bodies = off`); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	for pending := statements; len(pending) > 0; {
		failed, last := applyPass(ctx, transaction, pending)
		if len(failed) == len(pending) {
			return fmt.Errorf("apply schema: %w", last)
		}

		pending = failed
	}

	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}

	return nil
}

// applyPass runs each statement under a savepoint and returns the ones that
// failed, with the last error.
func applyPass(ctx context.Context, transaction pgx.Tx, statements []string) ([]string, error) {
	var (
		failed []string
		last   error
	)

	for _, statement := range statements {
		savepoint, err := transaction.Begin(ctx)
		if err != nil {
			return statements, fmt.Errorf("savepoint: %w", err)
		}

		if _, err := savepoint.Exec(ctx, statement); err != nil {
			last = fmt.Errorf("%s: %w", statement, errors.Join(err, savepoint.Rollback(ctx)))
			failed = append(failed, statement)

			continue
		}

		if err := savepoint.Commit(ctx); err != nil {
			return statements, fmt.Errorf("release savepoint: %w", err)
		}
	}

	return failed, last
}

// schemaStatements are the statements of the files, CREATE TABLE without its
// foreign keys, and those foreign keys as ALTER TABLE statements last.
func schemaStatements(parser parse.Parser, files []catalog.File) ([]string, error) {
	var statements, foreignKeys []string

	for _, file := range files {
		result, err := parser.Parse(file.SQL)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Name, err)
		}

		for _, raw := range result.GetStmts() {
			text := statementText(file.SQL, raw)

			create := raw.GetStmt().GetCreateStmt()
			if create == nil {
				statements = append(statements, text)

				continue
			}

			keys := detachForeignKeys(create)
			if len(keys) == 0 {
				statements = append(statements, text)

				continue
			}

			deparsed, err := deparse(parser, raw.GetStmt())
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file.Name, err)
			}

			statements = append(statements, deparsed)

			for _, key := range keys {
				alter, err := deparse(parser, addConstraint(create.GetRelation(), key))
				if err != nil {
					return nil, fmt.Errorf("%s: %w", file.Name, err)
				}

				foreignKeys = append(foreignKeys, alter)
			}
		}
	}

	return append(statements, foreignKeys...), nil
}

// statementText is a statement's source; the last one of a file has no
// length recorded and runs to the end.
func statementText(sql string, raw *pg.RawStmt) string {
	start := int(raw.GetStmtLocation())
	if raw.GetStmtLen() > 0 {
		return sql[start : start+int(raw.GetStmtLen())]
	}

	return sql[start:]
}

// detachForeignKeys removes the foreign keys of a CREATE TABLE and returns
// them as table constraints, a column constraint naming its column.
func detachForeignKeys(create *pg.CreateStmt) []*pg.Constraint {
	var (
		keys     []*pg.Constraint
		elements []*pg.Node
	)

	for _, element := range create.GetTableElts() {
		if constraint := element.GetConstraint(); constraint != nil &&
			constraint.GetContype() == pg.ConstrType_CONSTR_FOREIGN {
			keys = append(keys, constraint)

			continue
		}

		if column := element.GetColumnDef(); column != nil {
			var kept []*pg.Node

			for _, node := range column.GetConstraints() {
				constraint := node.GetConstraint()
				if constraint.GetContype() != pg.ConstrType_CONSTR_FOREIGN {
					kept = append(kept, node)

					continue
				}

				constraint.FkAttrs = []*pg.Node{pg.MakeStrNode(column.GetColname())}
				keys = append(keys, constraint)
			}

			column.Constraints = kept
		}

		elements = append(elements, element)
	}

	create.TableElts = elements

	return keys
}

func addConstraint(table *pg.RangeVar, constraint *pg.Constraint) *pg.Node {
	command := &pg.AlterTableCmd{
		Subtype: pg.AlterTableType_AT_AddConstraint, Def: &pg.Node{Node: &pg.Node_Constraint{Constraint: constraint}},
	}

	return &pg.Node{Node: &pg.Node_AlterTableStmt{AlterTableStmt: &pg.AlterTableStmt{
		Relation: table, Objtype: pg.ObjectType_OBJECT_TABLE,
		Cmds: []*pg.Node{{Node: &pg.Node_AlterTableCmd{AlterTableCmd: command}}},
	}}}
}

func deparse(parser parse.Parser, statement *pg.Node) (string, error) {
	text, err := parser.Deparse(&pg.ParseResult{Stmts: []*pg.RawStmt{{Stmt: statement}}})
	if err != nil {
		return "", fmt.Errorf("deparse: %w", err)
	}

	return text, nil
}
