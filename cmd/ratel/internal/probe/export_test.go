package probe

import "github.com/gopherex/ratel/cmd/ratel/internal/diff"

// Probed exposes probed to tests.
func Probed(kind diff.Kind) bool { return probed(kind) }
