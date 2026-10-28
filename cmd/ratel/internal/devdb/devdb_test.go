package devdb_test

import (
	"testing"

	"github.com/gopherex/ratel/cmd/ratel/internal/config"
	"github.com/gopherex/ratel/cmd/ratel/internal/devdb"
)

func TestStartRequiresServer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		settings config.Dev
	}{
		{name: "nothing configured"},
		{name: "unparsable url", settings: config.Dev{URL: "postgres://%zz"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if server, err := devdb.Start(t.Context(), tc.settings); err == nil || server != nil {
				t.Fatalf("Start() = %v, %v", server, err)
			}
		})
	}
}
