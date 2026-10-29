package ratel_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/pgtx/pkg/tx"

	"github.com/gopherex/ratel"
)

var (
	_ ratel.Executor = (*pgxpool.Pool)(nil)
	_ ratel.Executor = (*pgx.Conn)(nil)
	_ ratel.Executor = pgx.Tx(nil)
	_ ratel.Executor = tx.DB(nil)
)

func TestNew(t *testing.T) {
	t.Parallel()

	if ratel.New(&pgx.Conn{}) == nil {
		t.Fatal("New returned nil")
	}
}
