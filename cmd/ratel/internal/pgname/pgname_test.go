package pgname_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/pgname"
)

func TestObject(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("t", 60)

	for _, tc := range []struct{ name1, name2, label, want string }{
		{"a", "n", "check", "a_n_check"},
		{"a", "", "check", "a_check"},
		{long, "", "pkey", long[:58] + "_pkey"},
		{long, long, "key", long[:29] + "_" + long[:29] + "_key"},
		{"a", long, "key", "a_" + long[:57] + "_key"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			if got := pgname.Object(tc.name1, tc.name2, tc.label); got != tc.want {
				t.Errorf("Object() = %q", got)
			}
		})
	}
}

func TestChoose(t *testing.T) {
	t.Parallel()

	used := map[string]bool{"a_x_check": true, "a_x_check1": true}

	if got := pgname.Choose("a", "x", "check", func(name string) bool { return used[name] }); got != "a_x_check2" {
		t.Errorf("Choose() = %q", got)
	}
}

func TestAddition(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("c", 40)

	for _, tc := range []struct {
		columns []string
		want    string
	}{
		{nil, ""},
		{[]string{"x", "y"}, "x_y"},
		{[]string{long, long, long}, long + "_" + long},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			if got := pgname.Addition(tc.columns); got != tc.want {
				t.Errorf("Addition(%v) = %q", tc.columns, got)
			}
		})
	}
}

func TestClip(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value string
		limit int
		want  string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc"},
		{"абв", 3, "а"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()

			if got := pgname.Clip(tc.value, tc.limit); got != tc.want {
				t.Errorf("Clip(%q, %d) = %q", tc.value, tc.limit, got)
			}
		})
	}
}

func BenchmarkChoose(b *testing.B) {
	long := strings.Repeat("t", 60)
	used := func(name string) bool { return strings.HasSuffix(name, "_key") }

	for b.Loop() {
		pgname.Choose(long, pgname.Addition([]string{long, long}), "key", used)
	}
}
