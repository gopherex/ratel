package mapper_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/cmd/ratel/internal/mapper"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

func TestStatementProvenance(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct{ name, sql string }{
		{"select", "-- привет\nSELECT 1, (SELECT 2);\nSELECT 3"},
		{"cte", "WITH x AS (DELETE FROM t RETURNING id) SELECT * FROM x"},
		{"raw", "VACUUM app.t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			seen := make(map[string]bool)

			for index, raw := range tree.GetStmts() {
				current := mapper.New("query.sql", tc.sql, raw, index)
				first := current.MapStatement(raw.GetStmt())
				second := current.MapStatement(raw.GetStmt())

				if !proto.Equal(first, second) {
					t.Fatal("mapping is not deterministic")
				}

				span := first.GetMeta().GetSource()
				if span.GetFile() != "query.sql" || span.GetEndOffset() > uint64(len(tc.sql)) ||
					span.GetStartLine() == 0 || span.GetEndColumn() == 0 {
					t.Fatalf("invalid span: %v", span)
				}

				inspectIDs(t, first.ProtoReflect(), seen)
			}
		})
	}
}

func inspectIDs(t *testing.T, message protoreflect.Message, seen map[string]bool) {
	t.Helper()

	fields := message.Descriptor().Fields()
	if field := fields.ByName("node_id"); field != nil {
		id := message.Get(field).String()
		if id == "" || seen[id] {
			t.Fatalf("empty or duplicate node id %q", id)
		}

		seen[id] = true
	}

	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Message() == nil {
			return true
		}

		if field.IsList() {
			list := value.List()
			for index := range list.Len() {
				inspectIDs(t, list.Get(index).Message(), seen)
			}
		} else {
			inspectIDs(t, value.Message(), seen)
		}

		return true
	})
}

func TestSourceCoordinates(t *testing.T) {
	parse.Warm()
	t.Parallel()

	for _, tc := range []struct {
		sql                string
		endLine, endColumn uint32
		endOffset          uint64
	}{
		{"SELECT 'ёж'", 1, 12, 13},
		{"SELECT\n 1", 2, 3, 9},
		{"SELECT 1;", 1, 9, 8},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			t.Parallel()

			tree, err := parse.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}

			raw := tree.GetStmts()[0]
			current := mapper.New("source.sql", tc.sql, raw, 0)
			got := current.MapStatement(raw.GetStmt())

			span := got.GetMeta().GetSource()
			if span.GetStartOffset() != 0 || span.GetStartLine() != 1 || span.GetStartColumn() != 1 ||
				span.GetEndLine() != tc.endLine || span.GetEndColumn() != tc.endColumn ||
				span.GetEndOffset() != tc.endOffset {
				t.Fatal(span)
			}

			if got.GetSelect().GetRawSql() != current.SourceSQL() {
				t.Fatal("original SQL changed", got)
			}
		})
	}
}

func TestNilDeclarations(t *testing.T) {
	t.Parallel()

	current := mapper.New("nil.sql", "", nil, 0)

	for _, tc := range []struct {
		name  string
		empty func() bool
	}{
		{"expression", func() bool { return current.MapExpr(nil) == nil }},
		{"table", func() bool { return current.MapCreateTable(nil) == nil }},
		{"column", func() bool { return current.MapColumn(nil, 1) == nil }},
		{"constraint", func() bool { return current.MapConstraint(nil, "") == nil }},
		{"index", func() bool { return current.MapIndex(nil) == nil }},
		{"view", func() bool { return current.MapView(nil) == nil }},
		{"enum", func() bool { return current.MapCreateEnum(nil) == nil }},
		{"sequence", func() bool { return current.MapCreateSequence(nil) == nil }},
		{"domain", func() bool { return current.MapCreateDomain(nil) == nil }},
		{"composite", func() bool { return current.MapCompositeType(nil) == nil }},
		{"range", func() bool { return current.MapCreateRange(nil) == nil }},
		{"function", func() bool {
			function, procedure := current.MapCreateFunction(nil)

			return function == nil && procedure == nil
		}},
		{"trigger", func() bool { return current.MapCreateTrigger(nil) == nil }},
		{"materialized", func() bool { return current.MapMaterializedView(nil) == nil }},
		{"schema", func() bool { return current.MapCreateSchema(nil) == nil }},
		{"extension", func() bool { return current.MapCreateExtension(nil) == nil }},
		{"policy", func() bool { return current.MapCreatePolicy(nil) == nil }},
		{"grant", func() bool { return current.MapGrant(nil) == nil }},
		{"alter", func() bool { return current.MapAlterTable(nil) == nil }},
		{"comment", func() bool {
			object, metadata := current.MapComment(nil)

			return object == nil && metadata == nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if !tc.empty() {
				t.Fatal("nil declaration produced an object")
			}
		})
	}
}

func BenchmarkMapStatement(b *testing.B) {
	sql := "SELECT id, sum(amount) OVER (PARTITION BY id ORDER BY amount) FROM app.orders WHERE id = $1"

	tree, err := parse.Parse(sql)
	if err != nil {
		b.Fatal(err)
	}

	raw := tree.GetStmts()[0]
	current := mapper.New("bench.sql", sql, raw, 0)

	for b.Loop() {
		current.MapStatement(raw.GetStmt())
	}
}
