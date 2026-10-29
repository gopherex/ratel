package gengo_test

import (
	"slices"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
)

func TestScanCode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		columns       []gengo.ScanColumn
		targets       []string
		before, after string
	}{
		{
			name:    "plain fields scan directly",
			columns: []gengo.ScanColumn{{Target: "&model.ID"}, {Target: "&model.Email"}},
			targets: []string{"&model.ID", "&model.Email"},
		},
		{
			name: "pgtype temporary",
			columns: []gengo.ScanColumn{
				{Target: "&model.ID"},
				{Target: "&model.ManagerID", Temp: "pgtype.Int8", Field: "Int64", Value: "int64"},
			},
			targets: []string{"&model.ID", "&null1"},
			before:  "\tvar null1 pgtype.Int8\n\n",
			after:   "\tmodel.ManagerID = ratel.Null[int64]{V: null1.Int64, Valid: null1.Valid}\n\n",
		},
		{
			name:    "pointer temporary",
			columns: []gengo.ScanColumn{{Target: "&model.Spent", Temp: "*decimal.Decimal", Value: "decimal.Decimal"}},
			targets: []string{"&null0"},
			before:  "\tvar null0 *decimal.Decimal\n\n",
			after:   "\tmodel.Spent = ratel.NullFromPtr(null0)\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			targets, before, after := gengo.ScanCode(tc.columns)
			if !slices.Equal(targets, tc.targets) || before != tc.before || after != tc.after {
				t.Fatalf("ScanCode() = %q, %q, %q", targets, before, after)
			}
		})
	}
}

func TestProjection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		text, nullable bool
		element, want  string
	}{
		{name: "value", element: "int64", want: "out[index] = insert.id"},
		{name: "nullable", nullable: true, element: "*int64", want: "out[index] = insert.id.Ptr()"},
		{name: "enum", text: true, element: "string", want: "out[index] = string(insert.id)"},
		{
			name: "nullable enum", text: true, nullable: true, element: "*string",
			want: "if value := insert.id; value.Valid {\ntext := string(value.V)\nout[index] = &text\n}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			element, statement := gengo.Projection(tc.text, tc.nullable, "int64", "insert.id")
			if element != tc.element || statement != tc.want {
				t.Fatalf("Projection() = %q, %q", element, statement)
			}
		})
	}
}

func BenchmarkScanCode(b *testing.B) {
	columns := []gengo.ScanColumn{
		{Target: "&model.ID"},
		{Target: "&model.ManagerID", Temp: "pgtype.Int8", Field: "Int64", Value: "int64"},
		{Target: "&model.Spent", Temp: "*decimal.Decimal", Value: "decimal.Decimal"},
	}

	for b.Loop() {
		gengo.ScanCode(columns)
	}
}

func BenchmarkProjection(b *testing.B) {
	for b.Loop() {
		gengo.Projection(true, true, "int64", "insert.id")
	}
}
