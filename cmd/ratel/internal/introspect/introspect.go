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
	// Objects are the statements of SQL in order, each keyed by its object:
	// "table app.users", "index app.users_email_idx", "constraint
	// app.users.users_pkey", "routine app.f(integer)", "type app.status"...
	Objects []Object
	// Columns are column definitions by "schema.table.column".
	Columns map[string]Column
}

// Object is one rendered statement and the key of the object it creates.
type Object struct{ Key, SQL string }

// Column is a column as ADD COLUMN takes it, with its type and default.
type Column struct {
	Definition string
	Type       string
	Default    string // empty without one
}

// Statement returns the rendered statement of an object key.
func (r Rendering) Statement(key string) (string, bool) {
	for _, object := range r.Objects {
		if object.Key == key {
			return object.SQL, true
		}
	}

	return "", false
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
		objects, err := render(ctx, snapshot, section, namespaces)
		if err != nil {
			return Rendering{}, err
		}

		for _, object := range objects {
			out.WriteString(object.SQL)
			out.WriteString("\n")
		}

		result.Objects = append(result.Objects, objects...)
	}

	result.SQL = out.String()

	result.Columns, err = renderColumns(ctx, snapshot, namespaces)
	if err != nil {
		return Rendering{}, err
	}

	return result, nil
}

func renderColumns(ctx context.Context, snapshot pgx.Tx, namespaces []string) (map[string]Column, error) {
	rows, err := snapshot.Query(ctx, columns, namespaces)
	if err != nil {
		return nil, fmt.Errorf("introspect: columns: %w", err)
	}

	defer rows.Close()

	result := make(map[string]Column)

	for rows.Next() {
		var (
			key        string
			column     Column
			expression *string
		)

		if err := rows.Scan(&key, &column.Definition, &column.Type, &expression); err != nil {
			return nil, fmt.Errorf("introspect: columns: %w", err)
		}

		if expression != nil {
			column.Default = *expression
		}

		result[key] = column
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("introspect: columns: %w", err)
	}

	return result, nil
}

func render(ctx context.Context, snapshot pgx.Tx, section section, namespaces []string) ([]Object, error) {
	var arguments []any
	if !section.database {
		arguments = append(arguments, namespaces)
	}

	rows, err := snapshot.Query(ctx, section.query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("introspect: %s: %w", section.name, err)
	}

	objects, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Object, error) {
		var object Object

		return object, row.Scan(&object.Key, &object.SQL)
	})
	if err != nil {
		return nil, fmt.Errorf("introspect: %s: %w", section.name, err)
	}

	return objects, nil
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
