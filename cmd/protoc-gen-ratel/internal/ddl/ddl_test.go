package ddl_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	pg_query "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/gopherex/ratel/cmd/protoc-gen-ratel/internal/ddl"
)

// protoRoot holds ratel/v1/options/ratel.proto, imported by the fixtures.
const protoRoot = "../../../../proto"

// goldenDir holds the valid fixture cases: one directory per case, one golden
// .sql per .proto. RATEL_UPDATE_GOLDEN=1 rewrites the goldens.
const goldenDir = "testdata/valid"

// protoSet is one directory of .proto sources and the names to generate.
type protoSet struct {
	sources map[string]string
	names   []string
}

// compile builds the CodeGeneratorRequest protoc would send for the sources.
// The request is marshaled and unmarshaled, as the plugin receives it.
func compile(tb testing.TB, sources map[string]string, names []string) *pluginpb.CodeGeneratorRequest {
	tb.Helper()

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: accessor(sources),
		}),
	}

	compiled, err := compiler.Compile(context.Background(), names...)
	if err != nil {
		tb.Fatalf("compile %v: %v", names, err)
	}

	var files []*descriptorpb.FileDescriptorProto

	seen := make(map[string]bool)

	var add func(protoreflect.FileDescriptor)

	add = func(file protoreflect.FileDescriptor) {
		if seen[file.Path()] {
			return
		}

		seen[file.Path()] = true

		for index := range file.Imports().Len() {
			add(file.Imports().Get(index).FileDescriptor)
		}

		files = append(files, protodesc.ToFileDescriptorProto(file))
	}

	for _, file := range compiled {
		add(file)
	}

	raw, err := proto.Marshal(&pluginpb.CodeGeneratorRequest{FileToGenerate: names, ProtoFile: files})
	if err != nil {
		tb.Fatal(err)
	}

	request := new(pluginpb.CodeGeneratorRequest)
	if err := proto.Unmarshal(raw, request); err != nil {
		tb.Fatal(err)
	}

	return request
}

// accessor serves the in-memory sources and the repository's ratel options.
func accessor(sources map[string]string) func(string) (io.ReadCloser, error) {
	return func(path string) (io.ReadCloser, error) {
		if src, ok := sources[path]; ok {
			return io.NopCloser(strings.NewReader(src)), nil
		}

		return os.Open(filepath.Join(protoRoot, path))
	}
}

// generate runs the generator over a request and returns the output files.
func generate(tb testing.TB, request *pluginpb.CodeGeneratorRequest) (map[string]string, error) {
	tb.Helper()

	plugin, err := protogen.Options{}.New(request)
	if err != nil {
		tb.Fatal(err)
	}

	if err := ddl.Generate(plugin); err != nil {
		return nil, err
	}

	outputs := make(map[string]string)

	for _, file := range plugin.Response().GetFile() {
		outputs[file.GetName()] = file.GetContent()
	}

	return outputs, nil
}

// readFixture reads every .proto file of a fixture directory.
func readFixture(tb testing.TB, dir string) protoSet {
	tb.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatal(err)
	}

	out := protoSet{sources: make(map[string]string)}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".proto" {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			tb.Fatal(err)
		}

		out.sources[entry.Name()] = string(raw)
		out.names = append(out.names, entry.Name())
	}

	return out
}

// checkGolden parses one output as PostgreSQL and compares it with its golden
// file, or rewrites the golden when RATEL_UPDATE_GOLDEN=1.
func checkGolden(tb testing.TB, golden, content string) {
	tb.Helper()

	if _, err := pg_query.Parse(content); err != nil {
		tb.Errorf("%s does not parse as PostgreSQL: %v", golden, err)
	}

	if os.Getenv("RATEL_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(content), 0o600); err != nil {
			tb.Fatal(err)
		}

		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		tb.Fatal(err)
	}

	if content != string(want) {
		tb.Errorf("%s differs from golden:\n%s", golden, content)
	}
}

func TestGenerateGolden(t *testing.T) {
	t.Parallel()

	for _, caseName := range []string{"basic", "relations"} {
		t.Run(caseName, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(goldenDir, caseName)
			fixture := readFixture(t, dir)

			outputs, err := generate(t, compile(t, fixture.sources, fixture.names))
			if err != nil {
				t.Fatalf("generate: %v", err)
			}

			if len(outputs) != len(fixture.names) {
				t.Fatalf("got %d outputs for %d protos", len(outputs), len(fixture.names))
			}

			for name, content := range outputs {
				checkGolden(t, filepath.Join(dir, name), content)
			}
		})
	}
}

// header is the package, go_package and options import every inline fixture needs.
const header = `syntax = "proto3";
package t.v1;
option go_package = "example.com/t/v1;tv1";
import "ratel/v1/options/ratel.proto";
`

func TestGenerateInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{
			name:    "uint64 without a type",
			body:    `message T { option (ratel.v1.options.table) = {}; uint64 n = 1; }`,
			wantErr: ddl.ErrUnsupportedType,
		},
		{
			name:    "message field without embed or serialize",
			body:    `message A { string s = 1; } message T { option (ratel.v1.options.table) = {}; A a = 1; }`,
			wantErr: ddl.ErrUnsupportedType,
		},
		{
			name: "embed collides with a column",
			body: `message A { string street = 1; }
message T { option (ratel.v1.options.table) = {}; string street = 1;
  A a = 2 [(ratel.v1.options.column) = { embed: {} }]; }`,
			wantErr: ddl.ErrDuplicateColumn,
		},
		{
			name: "embed cycle",
			body: `message B { A a = 1 [(ratel.v1.options.column) = { embed: {} }]; }
message A { B b = 1 [(ratel.v1.options.column) = { embed: {} }]; }
message T { option (ratel.v1.options.table) = {}; B b = 1 [(ratel.v1.options.column) = { embed: {} }]; }`,
			wantErr: ddl.ErrEmbedCycle,
		},
		{
			name: "reference to an unknown message",
			body: `message T { option (ratel.v1.options.table) = {};
  int64 x = 1 [(ratel.v1.options.column) = { references: { table: "t.v1.Nope" columns: "id" } }]; }`,
			wantErr: ddl.ErrUnknownMessage,
		},
		{
			name: "reference to a message that is not a table",
			body: `message A { int64 id = 1; }
message T { option (ratel.v1.options.table) = {};
  int64 x = 1 [(ratel.v1.options.column) = { references: { table: "t.v1.A" columns: "id" } }]; }`,
			wantErr: ddl.ErrNotTable,
		},
		{
			name:    "primary key names an unknown field",
			body:    `message T { option (ratel.v1.options.table) = { primary_key: "nope" }; int64 id = 1; }`,
			wantErr: ddl.ErrUnknownField,
		},
		{
			name: "default with a statement end",
			body: `message T { option (ratel.v1.options.table) = {};
  int64 id = 1 [(ratel.v1.options.column) = { default: "1); DROP TABLE x" }]; }`,
			wantErr: ddl.ErrInvalidOption,
		},
		{
			name: "inverse without rel",
			body: `message A { option (ratel.v1.options.table) = {}; int64 id = 1; }
message T { option (ratel.v1.options.table) = {};
  int64 x = 1 [(ratel.v1.options.column) = { references: { table: "t.v1.A" columns: "id" inverse: "B" } }]; }`,
			wantErr: ddl.ErrInvalidOption,
		},
		{
			name: "generated column with a default",
			body: `message T { option (ratel.v1.options.table) = {};
  int64 id = 1 [(ratel.v1.options.column) = { generated: "1", default: "1" }]; }`,
			wantErr: ddl.ErrInvalidOption,
		},
		{
			name:    "table without columns",
			body:    `message T { option (ratel.v1.options.table) = {}; }`,
			wantErr: ddl.ErrInvalidOption,
		},
		{
			name: "serialize on a scalar",
			body: `message T { option (ratel.v1.options.table) = {};
  int64 id = 1 [(ratel.v1.options.column) = { serialize: true }]; }`,
			wantErr: ddl.ErrInvalidOption,
		},
		{
			name: "embed on a repeated field",
			body: `message A { int64 id = 1; }
message T { option (ratel.v1.options.table) = {};
  repeated A a = 1 [(ratel.v1.options.column) = { embed: {} }]; }`,
			wantErr: ddl.ErrInvalidOption,
		},
		{
			name: "two tables with one name",
			body: `message A { option (ratel.v1.options.table) = { name: "x" }; int64 id = 1; }
message B { option (ratel.v1.options.table) = { name: "x" }; int64 id = 1; }`,
			wantErr: ddl.ErrDuplicateObject,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sources := map[string]string{"t.proto": header + tc.body}

			_, err := generate(t, compile(t, sources, []string{"t.proto"}))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("generate error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func BenchmarkGenerate(b *testing.B) {
	fixture := readFixture(b, filepath.Join(goldenDir, "basic"))
	request := compile(b, fixture.sources, fixture.names)

	b.ResetTimer()

	for b.Loop() {
		plugin, err := protogen.Options{}.New(request)
		if err != nil {
			b.Fatal(err)
		}

		if err := ddl.Generate(plugin); err != nil {
			b.Fatal(err)
		}
	}
}
