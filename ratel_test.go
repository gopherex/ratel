package ratel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/gopherex/pgtx/pkg/tx"

	"github.com/gopherex/ratel"
)

var (
	_ ratel.Executor = (*pgxpool.Pool)(nil)
	_ ratel.Executor = (*pgx.Conn)(nil)
	_ ratel.Executor = pgx.Tx(nil)
	_ ratel.Executor = tx.DB(nil)
)

func TestNew(t *testing.T) {
	t.Parallel()

	if ratel.New(&pgx.Conn{}) == nil {
		t.Fatal("New returned nil")
	}
}

type observedProvider struct {
	trace.TracerProvider
	spans *[]*observedSpan
}

func (p observedProvider) Tracer(name string, options ...trace.TracerOption) trace.Tracer {
	return observedTracer{Tracer: p.TracerProvider.Tracer(name, options...), spans: p.spans}
}

type observedTracer struct {
	trace.Tracer
	spans *[]*observedSpan
}

func (t observedTracer) Start(ctx context.Context, _ string, options ...trace.SpanStartOption) (
	context.Context, trace.Span,
) {
	config := trace.NewSpanStartConfig(options...)
	observed := &observedSpan{Span: trace.SpanFromContext(ctx), attributes: config.Attributes()}
	*t.spans = append(*t.spans, observed)

	return trace.ContextWithSpan(ctx, observed), observed
}

type observedSpan struct {
	trace.Span
	attributes []attribute.KeyValue
	ended      bool
	status     codes.Code
}

func (s *observedSpan) End(options ...trace.SpanEndOption) {
	s.ended = true
	s.Span.End(options...)
}

func (s *observedSpan) SetStatus(code codes.Code, description string) {
	s.status = code
	s.Span.SetStatus(code, description)
}

func TestQuerySourceTelemetry(t *testing.T) {
	t.Parallel()

	var spans []*observedSpan

	provider := observedProvider{TracerProvider: noop.NewTracerProvider(), spans: &spans}
	executor := readExecutor{query: func(context.Context, string, ...any) (pgx.Rows, error) {
		return &readRows{}, nil
	}}
	source := ratel.NewQuery([]string{"id", "email", "manager_id"}, scanReadUser,
		"SELECT 1::bigint AS id, ''::text AS email, NULL::bigint AS manager_id")

	if _, err := ratel.New(executor, ratel.WithTracerProvider(provider)).From(source).List(t.Context()); err != nil {
		t.Fatal(err)
	}

	for _, attribute := range spans[0].attributes {
		if attribute.Key == "db.collection.name" {
			t.Fatal("static query has no known physical collection")
		}
	}
}

func TestReadTelemetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		failure error
		invalid bool
		status  codes.Code
	}{
		{"success", nil, false, codes.Unset},
		{"failure", errors.New("sensitive driver failure"), false, codes.Error},
		{"invalid", nil, true, codes.Unset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var spans []*observedSpan

			provider := observedProvider{TracerProvider: noop.NewTracerProvider(), spans: &spans}
			executor := readExecutor{query: func(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
				if len(spans) != 1 || trace.SpanFromContext(ctx) != spans[0] {
					t.Fatal("query did not receive operation span context")
				}

				return &readRows{}, tc.failure
			}}
			users := userDescriptor()

			query := ratel.New(executor, ratel.WithTracerProvider(provider)).From(users).Where(users.Email.Eq("secret"))
			if tc.invalid {
				query = query.Limit(-1)
			}

			_, err := query.List(t.Context())
			if !tc.invalid && !errors.Is(err, tc.failure) {
				t.Fatal(err)
			}

			if len(spans) != 1 || !spans[0].ended || spans[0].status != tc.status {
				t.Fatalf("spans: %+v", spans)
			}

			for _, attribute := range spans[0].attributes {
				if attribute.Key == "db.query.text" || attribute.Value.AsString() == "secret" {
					t.Fatalf("sensitive telemetry: %v", attribute)
				}
			}
		})
	}
}
