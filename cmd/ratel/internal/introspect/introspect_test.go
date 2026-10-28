package introspect_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
)

var errUnavailable = errors.New("unavailable")

type unavailable struct{}

func (unavailable) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errUnavailable
}

func TestLoadFailure(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	result, err := introspect.Load(t.Context(), parser, unavailable{}, []string{"public"})
	if !errors.Is(err, errUnavailable) || result != nil {
		t.Fatalf("Load() = %v, %v; want no catalog and the cause", result, err)
	}
}

func TestClearSources(t *testing.T) {
	t.Parallel()

	span := &ir.SourceSpan{File: "introspect.sql", StartLine: 1}

	for _, tc := range []struct {
		name    string
		catalog *ir.Catalog
		spans   func(*ir.Catalog) []*ir.SourceSpan
	}{
		{
			name:    "nested message",
			catalog: &ir.Catalog{Meta: &ir.Metadata{Source: span, Comment: "kept"}},
			spans:   func(c *ir.Catalog) []*ir.SourceSpan { return []*ir.SourceSpan{c.GetMeta().GetSource()} },
		},
		{
			name:    "list element",
			catalog: &ir.Catalog{Schemas: []*ir.Schema{{Name: "s", Tables: []*ir.Table{{Meta: &ir.Metadata{Source: span}}}}}},
			spans: func(c *ir.Catalog) []*ir.SourceSpan {
				return []*ir.SourceSpan{c.GetSchemas()[0].GetTables()[0].GetMeta().GetSource()}
			},
		},
		{
			name:    "no spans",
			catalog: &ir.Catalog{Name: "db"},
			spans:   func(*ir.Catalog) []*ir.SourceSpan { return nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			introspect.ClearSources(tc.catalog.ProtoReflect())

			for _, remaining := range tc.spans(tc.catalog) {
				if remaining != nil {
					t.Fatalf("span kept: %v", remaining)
				}
			}

			if tc.catalog.GetMeta() != nil && tc.catalog.GetMeta().GetComment() != "kept" {
				t.Fatal("non-span field cleared")
			}
		})
	}
}

func BenchmarkClearSources(b *testing.B) {
	tables := make([]*ir.Table, 0, 100)
	for range 100 {
		tables = append(tables, &ir.Table{Meta: &ir.Metadata{}, Columns: []*ir.Column{{Meta: &ir.Metadata{}}}})
	}

	catalog := &ir.Catalog{Schemas: []*ir.Schema{{Tables: tables}}}

	for b.Loop() {
		introspect.ClearSources(catalog.ProtoReflect())
	}
}
