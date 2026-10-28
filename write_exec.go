package ratel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// WithBatchRows bounds rows per insert statement. Default: 1000. Nonpositive
// values reject inserts before execution. Chunk atomicity belongs to the executor.
func WithBatchRows(rows int) Option {
	return func(database *DB) { database.batchRows = rows }
}

type writeResult[M any] struct {
	count  int64
	models []M
}

func executeWrite[M any](ctx context.Context, database *DB, table Table[M], operation string,
	statements []Statement, returning bool, options []CallOption,
) (writeResult[M], error) {
	if database == nil {
		return writeResult[M]{}, ErrInvalidQuery
	}

	ctx, span := database.tracer.Start(ctx, "ratel.write", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.system.name", "postgresql"),
			attribute.String("db.operation.name", operation),
			attribute.String("db.collection.name", table.schema+"."+table.name)))
	defer span.End()

	result := writeResult[M]{models: make([]M, 0)}

	for _, statement := range statements {
		next, err := executeWriteStatement(ctx, database, table, statement, returning, options)
		if err != nil {
			return writeResult[M]{}, failure(span, err)
		}

		result.count += next.count
		result.models = append(result.models, next.models...)
	}

	return result, nil
}

func executeWriteStatement[M any](ctx context.Context, database *DB, table Table[M],
	statement Statement, returning bool, options []CallOption,
) (writeResult[M], error) {
	args := database.arguments(options, statement.Args)

	if !returning {
		tag, err := database.executor.Exec(ctx, statement.SQL, args...)

		return writeResult[M]{count: tag.RowsAffected()}, mapWriteError(err, table)
	}

	return collectWrite(ctx, database, table, statement.SQL, args)
}

func collectWrite[M any](ctx context.Context, database *DB, table Table[M],
	sql string, args []any,
) (writeResult[M], error) {
	writer := newSQLWriter()

	for index, column := range table.columns {
		if index > 0 {
			writer.WriteString(", ")
		}

		writer.column(column)
	}

	rows, err := database.executor.Query(ctx, sql+" RETURNING "+writer.String(), args...)
	if err != nil {
		return writeResult[M]{}, mapWriteError(err, table)
	}
	defer rows.Close()

	models := make([]M, 0)

	for rows.Next() {
		model, scanErr := table.scan(rows)
		if scanErr != nil {
			return writeResult[M]{}, mapWriteError(scanErr, table)
		}

		models = append(models, model)
	}

	if err = rows.Err(); err != nil {
		return writeResult[M]{}, mapWriteError(err, table)
	}

	return writeResult[M]{count: rows.CommandTag().RowsAffected(), models: models}, nil
}
