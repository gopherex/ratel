// Package introspect reads a live PostgreSQL database into the Catalog shape
// that catalog.Build produces from DDL.
//
// PostgreSQL renders each object as declarative DDL through its own deparse
// functions (format_type, pg_get_expr, pg_get_constraintdef, pg_get_indexdef,
// pg_get_viewdef, pg_get_functiondef, pg_get_triggerdef), and that text goes
// through catalog.Build. One mapping path serves source and database, so both
// sides have the same shape by construction; PostgreSQL's normalization of
// expressions is the only difference.
package introspect

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/ir"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

// File is the synthetic source name of the rendered DDL in diagnostics.
const File = "introspect.sql"

// Source opens the snapshot transaction. *pgx.Conn and *pgxpool.Pool satisfy
// it; a transaction does not, since its isolation is already fixed.
type Source interface {
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// Load reads the namespaces in one repeatable-read snapshot. It returns no
// catalog on any failure.
func Load(ctx context.Context, parser parse.Parser, source Source, namespaces []string) (*ir.Catalog, error) {
	rendering, err := Render(ctx, source, namespaces)
	if err != nil {
		return nil, err
	}

	snapshot, err := builtins.For(rendering.Major)
	if err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}

	files := []catalog.File{{Name: File, SQL: rendering.SQL}}

	result, err := catalog.Build(parser, files, rendering.Major, snapshot)
	if err != nil {
		return nil, fmt.Errorf("introspect: build rendered DDL: %w", err)
	}

	clearSources(result.ProtoReflect())
	result.Name = rendering.Database

	return result, nil
}

// Rendering is a database rendered as declarative DDL.
type Rendering struct {
	SQL      string
	Major    int    // server major version
	Database string // database name
}

// Render returns the namespaces as DDL.
func Render(ctx context.Context, source Source, namespaces []string) (Rendering, error) {
	snapshot, err := source.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Rendering{}, fmt.Errorf("introspect: begin: %w", err)
	}
	// Rollback ends a read-only snapshot; its error carries nothing to report.
	defer func() { _ = snapshot.Rollback(context.WithoutCancel(ctx)) }()

	// An empty search_path makes every deparse function schema-qualify names.
	if _, err := snapshot.Exec(ctx, `SET LOCAL search_path = ''`); err != nil {
		return Rendering{}, fmt.Errorf("introspect: search_path: %w", err)
	}

	var result Rendering

	err = snapshot.QueryRow(ctx,
		`SELECT current_setting('server_version_num')::int / 10000, current_database()`,
	).Scan(&result.Major, &result.Database)
	if err != nil {
		return Rendering{}, fmt.Errorf("introspect: server: %w", err)
	}

	var out strings.Builder

	for _, section := range sections() {
		if err := render(ctx, snapshot, section, namespaces, &out); err != nil {
			return Rendering{}, err
		}
	}

	result.SQL = out.String()

	return result, nil
}

func render(ctx context.Context, snapshot pgx.Tx, section section, namespaces []string, out *strings.Builder) error {
	var arguments []any
	if !section.database {
		arguments = append(arguments, namespaces)
	}

	rows, err := snapshot.Query(ctx, section.query, arguments...)
	if err != nil {
		return fmt.Errorf("introspect: %s: %w", section.name, err)
	}

	statements, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("introspect: %s: %w", section.name, err)
	}

	for _, statement := range statements {
		out.WriteString(statement)
		out.WriteString("\n")
	}

	return nil
}

// clearSources drops source positions: they point into the rendered text,
// and introspected objects carry none.
func clearSources(message protoreflect.Message) {
	span := (&ir.SourceSpan{}).ProtoReflect().Descriptor().FullName()

	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.Message() == nil:
		case field.Message().FullName() == span:
			message.Clear(field)
		case field.IsList():
			list := value.List()
			for index := range list.Len() {
				clearSources(list.Get(index).Message())
			}
		case field.IsMap():
		default:
			clearSources(value.Message())
		}

		return true
	})
}
