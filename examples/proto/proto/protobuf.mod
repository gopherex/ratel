module github.com/gopherex/ratel/examples/proto/proto

// The ratel options come from the repository's own proto module, as go.work
// resolves the Go modules of this repository.
require github.com/gopherex/ratel/proto

replace github.com/gopherex/ratel/proto => ../../../proto
