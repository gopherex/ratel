package parse

import (
	"testing"
	"testing/synctest"

	pg "github.com/pganalyze/pg_query_go/v6"
)

// CheckConcurrencyLimit exercises saturated semaphores without extra WASM instances.
func CheckConcurrencyLimit(t *testing.T, parser Parser) {
	t.Helper()

	for _, tc := range []struct {
		name string
		run  func(Parser) error
	}{
		{"parse", func(parser Parser) error {
			_, err := parser.Parse("SELECT 1")

			return err
		}},
		{"scan", func(parser Parser) error {
			_, err := parser.Scan("SELECT 1")

			return err
		}},
		{"deparse", func(parser Parser) error {
			_, err := parser.Deparse(&pg.ParseResult{})

			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Serialize real WASM calls while each bubble tests its own blocked gate.
			parser.slots <- struct{}{}

			t.Cleanup(func() { <-parser.slots })

			synctest.Test(t, func(t *testing.T) {
				limited := Parser{slots: make(chan struct{}, cap(parser.slots))}
				limited.slots <- struct{}{}

				done := make(chan error, 1)
				go func() { done <- tc.run(limited) }()
				synctest.Wait()

				select {
				case err := <-done:
					t.Fatalf("operation bypassed the concurrency bound: %v", err)
				default:
				}

				<-limited.slots

				if err := <-done; err != nil {
					t.Fatal(err)
				}

				if len(limited.slots) != 0 {
					t.Fatal("operation leaked a semaphore slot")
				}
			})
		})
	}
}

// CheckConfig verifies constructor bounds before any parallel WASM work starts.
func CheckConfig(t *testing.T) {
	t.Helper()

	for _, concurrency := range []int{0, 1, 2} {
		parser, err := New(Config{Concurrency: concurrency})
		if err != nil {
			t.Fatal(err)
		}

		if cap(parser.slots) != max(concurrency, DefaultConcurrency) {
			t.Fatalf("New(%d) capacity = %d", concurrency, cap(parser.slots))
		}
	}
}
