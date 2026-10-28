package gengo_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/gopherex/ratel/ir"
	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
	"github.com/gopherex/ratel/cmd/ratel/internal/query"
)

const options = `{"import_path": "example.com/app/db"}`

// TestGolden generates each testdata/<case>/schema.sql and compares every file
// with testdata/<case>/golden/<path>.golden. RATEL_UPDATE_GOLDEN=1 rewrites
// the goldens.
func TestGolden(t *testing.T) {
	parser := newParser(t)

	t.Parallel()

	cases, err := filepath.Glob("testdata/*/schema.sql")
	if err != nil || len(cases) == 0 {
		t.Fatalf("no golden cases: %v", err)
	}

	for _, schemaPath := range cases {
		directory := filepath.Dir(schemaPath)

		t.Run(filepath.Base(directory), func(t *testing.T) {
			t.Parallel()

			files := generate(t, parser, schemaPath)
			for _, file := range files {
				golden := filepath.Join(directory, "golden", file.GetPath()+".golden")
				compare(t, golden, file.GetContents())
			}
		})
	}
}

func TestOptions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, options, failure string }{
		{name: "missing import path", options: `{}`, failure: "import_path is required"},
		{name: "null options", options: `null`, failure: "import_path is required"},
		{name: "unknown key", options: `{"import_path": "x.com/db", "package": "db"}`, failure: "invalid options"},
		{name: "trailing slash", options: `{"import_path": "x.com/db/"}`, failure: "import_path is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := gengo.Generator{}.Generate(context.Background(), &pluginpb.GenerateRequest{
				Options: []byte(tc.options),
			})
			if err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("Generate() error = %v, want %q", err, tc.failure)
			}
		})
	}
}

func generate(tb testing.TB, parser parse.Parser, schemaPath string) []*pluginpb.File {
	tb.Helper()

	ddl, err := os.ReadFile(schemaPath)
	if err != nil {
		tb.Fatal(err)
	}

	signatures, err := builtins.For(18)
	if err != nil {
		tb.Fatal(err)
	}

	built, err := catalog.Build(parser, []catalog.File{{Name: "schema.sql", SQL: string(ddl)}}, 18, signatures, gengo.Name)
	if err != nil {
		tb.Fatal(err)
	}

	if err := query.Views(parser, built, signatures); err != nil {
		tb.Fatal(err)
	}

	request := &pluginpb.GenerateRequest{
		ProtocolVersion: 1, Catalog: built, Options: []byte(options), Annotations: namespaced(built.ProtoReflect()),
	}

	response := plugin.Dispatch(context.Background(), gengo.Generator{}, &pluginpb.PluginRequest{
		ProtocolVersion: 1, Request: &pluginpb.PluginRequest_Generate{Generate: request},
	})
	if plugin.HasErrors(response) {
		tb.Fatalf("Generate() diagnostics: %v", response.GetDiagnostics())
	}

	return response.GetGenerate().GetFiles()
}

// namespaced collects the catalog's go annotations, as the host does.
func namespaced(message protoreflect.Message) []*ir.AnnotationValue {
	var out []*ir.AnnotationValue

	switch meta := message.Interface().(type) {
	case *ir.Metadata:
		for _, annotation := range meta.GetAnnotations() {
			if annotation.GetNamespace() == gengo.Name {
				out = append(out, annotation)
			}
		}
	default:
	}

	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.Message() == nil || field.IsMap():
		case field.IsList():
			for index := range value.List().Len() {
				out = append(out, namespaced(value.List().Get(index).Message())...)
			}
		default:
			out = append(out, namespaced(value.Message())...)
		}

		return true
	})

	return out
}

func compare(t *testing.T, golden string, got []byte) {
	t.Helper()

	if os.Getenv("RATEL_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}

		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(want, got) {
		t.Fatalf("%s differs; run RATEL_UPDATE_GOLDEN=1 go test ./internal/gengo and review the diff\n%s", golden, got)
	}
}

func newParser(tb testing.TB) parse.Parser {
	tb.Helper()

	parser, err := parse.New(parse.Config{})
	if err != nil {
		tb.Fatal(err)
	}

	return parser
}

func BenchmarkGenerate(b *testing.B) {
	parser := newParser(b)

	for b.Loop() {
		generate(b, parser, "testdata/tables/schema.sql")
	}
}
