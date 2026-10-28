package plan_test

import (
	"strings"
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/plan"
)

func TestNames(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("n", 60)

	for _, tc := range []struct{ got, want string }{
		{plan.Generated("app.t", "_x"), "t_x"},
		{plan.Generated(`app."T"`, "_x"), `"T_x"`},
		{plan.Generated("app."+long, "_ratel_new"), long[:53] + "_ratel_new"},
		{plan.LastPart(`app."a.b"`), `"a.b"`},
		{plan.LastPart("app"), "app"},
		{plan.SchemaOf(`"my app".t`), `"my app"`},
		{plan.Unquote(`"say ""hi"""`), `say "hi"`},
		{plan.Unquote("plain"), "plain"},
		{plan.Quote("plain_1"), "plain_1"},
		{plan.Quote("Mixed"), `"Mixed"`},
		{plan.Quote(`a"b`), `"a""b"`},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func BenchmarkGenerated(b *testing.B) {
	for b.Loop() {
		plan.Generated(`app."Some Table"`, "_ratel_new")
	}
}
