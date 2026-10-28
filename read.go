package ratel

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// All lazily streams rows. Breaking iteration always releases the cursor.
// Errors are yielded once with a zero model; no further rows follow an error.
func (q Select[M]) All(ctx context.Context, options ...CallOption) iter.Seq2[M, error] {
	return func(yield func(M, error) bool) {
		q.iterate(ctx, "ratel.select.all", options, yield)
	}
}

func (q Select[M]) iterate(ctx context.Context, operation string, options []CallOption, yield func(M, error) bool) {
	var zero M

	if q.db == nil {
		yield(zero, ErrInvalidQuery)

		return
	}

	ctx, span := q.db.tracer.Start(ctx, operation, q.spanOptions()...)
	defer span.End()

	rows, err := q.rows(ctx, options)
	if err != nil {
		recordFailure(span, err)
		yield(zero, err)

		return
	}

	defer rows.Close()

	for rows.Next() {
		model, scanErr := q.source.table.scan(rows)
		if scanErr != nil {
			err = fmt.Errorf("ratel: scan SELECT: %w", scanErr)
			recordFailure(span, err)
			yield(zero, err)

			return
		}

		if !yield(model, nil) {
			return
		}
	}

	if err = rows.Err(); err != nil {
		err = fmt.Errorf("ratel: iterate SELECT: %w", err)
		recordFailure(span, err)
		yield(zero, err)
	}
}

func (q Select[M]) rows(ctx context.Context, options []CallOption) (pgx.Rows, error) {
	statement, err := q.SQL()
	if err != nil {
		return nil, err
	}

	args := statement.Args

	settings := callSettings{mode: q.db.execMode}
	for _, option := range options {
		settings = option(settings)
	}

	if settings.mode.Set {
		args = append([]any{settings.mode.V}, args...)
	}

	rows, err := q.db.executor.Query(ctx, statement.SQL, args...)
	if err != nil {
		return nil, fmt.Errorf("ratel: query SELECT: %w", err)
	}

	return rows, nil
}

// List collects the result. Empty results are a non-nil empty slice.
// An error returns no partial result.
func (q Select[M]) List(ctx context.Context, options ...CallOption) ([]M, error) {
	return q.collect(ctx, "ratel.select.list", options)
}

func (q Select[M]) collect(ctx context.Context, operation string, options []CallOption) ([]M, error) {
	if q.db == nil {
		return nil, ErrInvalidQuery
	}

	ctx, span := q.db.tracer.Start(ctx, operation, q.spanOptions()...)
	defer span.End()

	rows, err := q.rows(ctx, options)
	if err != nil {
		recordFailure(span, err)

		return nil, err
	}

	defer rows.Close()

	models := make([]M, 0)

	for rows.Next() {
		model, scanErr := q.source.table.scan(rows)
		if scanErr != nil {
			err = fmt.Errorf("ratel: scan SELECT: %w", scanErr)
			recordFailure(span, err)

			return nil, err
		}

		models = append(models, model)
	}

	if err = rows.Err(); err != nil {
		err = fmt.Errorf("ratel: iterate SELECT: %w", err)
		recordFailure(span, err)

		return nil, err
	}

	return models, nil
}

// One requires exactly one row. It returns ErrNotFound or ErrMultipleRows otherwise.
// At most two rows are requested, without increasing an existing lower limit.
func (q Select[M]) One(ctx context.Context, options ...CallOption) (M, error) {
	const detectMultiple = 2

	return q.single(ctx, "ratel.select.one", detectMultiple, options)
}

// First returns the first row in query order, or ErrNotFound. Without OrderBy,
// PostgreSQL chooses the row; callers needing determinism must supply an order.
func (q Select[M]) First(ctx context.Context, options ...CallOption) (M, error) {
	return q.single(ctx, "ratel.select.first", 1, options)
}

func (q Select[M]) single(ctx context.Context, operation string, limit int, options []CallOption) (M, error) {
	var zero M

	if !q.limit.Set || q.limit.V > limit {
		q = q.Limit(limit)
	}

	models, err := q.collect(ctx, operation, options)
	if err != nil {
		return zero, err
	}

	if len(models) == 0 {
		return zero, ErrNotFound
	}

	if len(models) > 1 {
		return zero, ErrMultipleRows
	}

	return models[0], nil
}

func (q Select[M]) spanOptions() []trace.SpanStartOption {
	return q.source.table.readOptions
}

func readSpanOptions(schema, table string) []trace.SpanStartOption {
	attributes := []attribute.KeyValue{
		attribute.String("db.system.name", "postgresql"),
		attribute.String("db.operation.name", "SELECT"),
		attribute.String("db.collection.name", schema+"."+table),
	}
	if table == "" {
		attributes = attributes[:len(attributes)-1]
	}

	return []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attributes...),
	}
}

func readRefusal(err error) bool {
	// A refusal must not hide technical failures from later batch results.
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		causes := joined.Unwrap()
		for _, child := range causes {
			if !readRefusal(child) {
				return false
			}
		}

		return len(causes) > 0
	}

	return errors.Is(err, ErrInvalidQuery) || errors.Is(err, ErrMultipleRows) || errors.Is(err, ErrNotFound)
}

func recordFailure(span trace.Span, err error) {
	if readRefusal(err) {
		return
	}

	// Driver errors can contain query values, so telemetry records no error text.
	span.SetStatus(codes.Error, "database operation failed")
}
