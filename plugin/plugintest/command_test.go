package plugintest_test

import (
	"testing"
	"time"

	"github.com/gopherex/ratel/plugin"
	"github.com/gopherex/ratel/plugin/pluginpb"
	"github.com/gopherex/ratel/plugin/plugintest"
)

func TestCommandFailure(t *testing.T) {
	t.Parallel()

	exchange := plugintest.Command("ratel-plugin-does-not-exist", time.Second)
	if _, err := exchange(t.Context(), plugin.Binary, new(pluginpb.PluginRequest)); err == nil {
		t.Fatal("missing executable accepted")
	}
}
