package intent_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/gopherex/ratel/cmd/ratel/internal/builtins"
	"github.com/gopherex/ratel/cmd/ratel/internal/catalog"
	"github.com/gopherex/ratel/cmd/ratel/internal/ident"
	"github.com/gopherex/ratel/cmd/ratel/internal/intent"
	"github.com/gopherex/ratel/cmd/ratel/internal/parse"
)

const schema = `CREATE SCHEMA app;
-- @ratel.renamed from=Old
-- @ratel.allow RTL-R03 reason=small
-- @ratel.allow RTL-C01 reason=agreed
CREATE TABLE app."Order" (
  id bigint PRIMARY KEY,
  -- @ratel.renamed from=before
  after text,
  -- @ratel.retype using=amount::numeric
  amount numeric,
  -- @ratel.backfill expr='x'
  note text NOT NULL
);
-- @ratel.renamed from=mood
-- @ratel.renamed from=fine value=ok
-- @ratel.renamed from=sad value="it's \ bad"
CREATE TYPE app.feeling AS ENUM ('ok', E'it''s \\ bad');`

func TestFrom(t *testing.T) {
	parser, err := parse.New(parse.Config{})
	if err != nil {
		t.Fatal(err)
	}

	t.Parallel()

	snapshot, err := builtins.For(18)
	if err != nil {
		t.Fatal(err)
	}

	built, err := catalog.Build(parser, []catalog.File{{Name: "s.sql", SQL: schema}}, 18, snapshot)
	if err != nil {
		t.Fatal(err)
	}

	got := intent.From(built, ident.New(parser))

	want := map[string]any{
		"renamed": map[string]string{
			`table app."Order"`: `"Old"`, `column app."Order".after`: "before", "type app.feeling": "mood",
			"value app.feeling 'ok'": "'fine'", `value app.feeling E'it''s \\ bad'`: "'sad'",
		},
		"retype":   map[string]string{`column app."Order".amount`: "amount::numeric"},
		"backfill": map[string]string{`column app."Order".note`: "'x'"},
		"allow":    map[string][]string{`table app."Order"`: {"RTL-C01", "RTL-R03"}},
	}

	if difference := cmp.Diff(want, map[string]any{
		"renamed": got.Diff.Renamed, "retype": got.Diff.Retype, "backfill": got.Plan.Backfill, "allow": got.Plan.Allow,
	}); difference != "" {
		t.Errorf("From() (-want +got):\n%s", difference)
	}

	if cmp.Diff(got.Diff.Retype, got.Plan.Retype) != "" {
		t.Error("plan and diff retype intents differ")
	}
}

func BenchmarkFrom(b *testing.B) {
	parser, err := parse.New(parse.Config{})
	if err != nil {
		b.Fatal(err)
	}

	built, err := catalog.Build(parser, []catalog.File{{Name: "s.sql", SQL: schema}}, 18, nil)
	if err != nil {
		b.Fatal(err)
	}

	quoter := ident.New(parser)

	for b.Loop() {
		intent.From(built, quoter)
	}
}
