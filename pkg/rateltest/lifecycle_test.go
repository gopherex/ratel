package rateltest_test

import (
	"slices"
	"testing"

	"github.com/gopherex/ratel/pkg/rateltest"
)

// A benchmark iteration must release its fixture before the next iteration;
// retaining b.Cleanup callbacks until benchmark exit would grow without bound.
type fixtureTB struct {
	testing.TB
	cleanups []func()
}

func (fixture *fixtureTB) Cleanup(cleanup func()) {
	fixture.cleanups = append(fixture.cleanups, cleanup)
}

func (fixture *fixtureTB) close() {
	for _, cleanup := range slices.Backward(fixture.cleanups) {
		cleanup()
	}

	fixture.cleanups = nil
}

func TestFixtureCleanup(t *testing.T) {
	t.Parallel()

	fixture := &fixtureTB{TB: t}

	var order []int

	fixture.Cleanup(func() { order = append(order, 1) })
	fixture.Cleanup(func() { order = append(order, 2) })
	fixture.close()
	fixture.close()

	if !slices.Equal(order, []int{2, 1}) {
		t.Fatalf("cleanup order = %v", order)
	}
}

func BenchmarkNewExecutor(b *testing.B) {
	for b.Loop() {
		fixture := &fixtureTB{TB: b}
		_ = rateltest.NewExecutor(fixture)
		fixture.close()
	}
}

func BenchmarkRollbackTx(b *testing.B) {
	for b.Loop() {
		fixture := &fixtureTB{TB: b}
		executor := rateltest.NewExecutor(fixture)
		executor.ExpectBegin()
		executor.ExpectRollback()

		_ = rateltest.RollbackTx(fixture, executor)

		fixture.close()
	}
}
