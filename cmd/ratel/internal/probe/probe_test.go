package probe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/cmd/ratel/internal/diff"
	"github.com/gopherex/ratel/cmd/ratel/internal/introspect"
	"github.com/gopherex/ratel/cmd/ratel/internal/probe"
)

func TestProbed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		kind diff.Kind
		want bool
	}{
		{diff.AddColumn, true},
		{diff.AlterColumnType, true},
		{diff.AlterCollation, true},
		{diff.AlterGenerated, true},
		{diff.SetDefault, false},
		{diff.CreateIndex, false},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			t.Parallel()

			if probe.Probed(tc.kind) != tc.want {
				t.Errorf("probed(%s) = %v", tc.kind, !tc.want)
			}
		})
	}
}

var errUnreachable = errors.New("unreachable")

type unreachable struct{}

func (unreachable) Begin(context.Context) (pgx.Tx, error) { return nil, errUnreachable }

func TestEnvFailsWithoutDatabase(t *testing.T) {
	t.Parallel()

	_, err := probe.Env(t.Context(), unreachable{}, introspect.Rendering{}, introspect.Rendering{}, nil, nil)
	if !errors.Is(err, errUnreachable) {
		t.Fatalf("Env() error = %v", err)
	}
}

func BenchmarkProbed(b *testing.B) {
	for b.Loop() {
		probe.Probed(diff.AlterColumnType)
	}
}
