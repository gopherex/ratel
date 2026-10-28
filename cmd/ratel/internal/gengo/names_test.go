package gengo_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/gengo"
)

func TestNames(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		got  func(string) string
		in   string
		want string
	}{
		{"pascal snake", gengo.Pascal, "manager_id", "ManagerID"},
		{"pascal camel", gengo.Pascal, "getUserURL", "GetUserURL"},
		{"pascal digits", gengo.Pascal, "line_2", "Line2"},
		{"singular", gengo.Singular, "users", "User"},
		{"singular ies", gengo.Singular, "categories", "Category"},
		{"singular irregular", gengo.Singular, "people", "Person"},
		{"plural", gengo.Plural, "user", "Users"},
		{"plural already", gengo.Plural, "orders", "Orders"},
		{"package", gengo.PackageName, "App_Data", "appdata"},
		{"package digit", gengo.PackageName, "2024", "p2024"},
		{"package empty", gengo.PackageName, "_", "p"},
		{"enum label dash", gengo.EnumLabel, "on-hold", "OnHold"},
		{"enum label empty", gengo.EnumLabel, "", "Empty"},
		{"parameter keyword", gengo.Parameter, "type", "typeValue"},
		{"parameter", gengo.Parameter, "manager_id", "managerID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.got(tc.in); got != tc.want {
				t.Fatalf("%s(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
			}
		})
	}
}

func BenchmarkPascal(b *testing.B) {
	for b.Loop() {
		gengo.Pascal("account_manager_id")
	}
}
