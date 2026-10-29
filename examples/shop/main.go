// Command shop is the ratel quick start: a schema, two queries, planned
// migrations applied on startup, and a few reads and writes.
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/gopherex/ratel"
	"github.com/gopherex/ratel/migrate"

	"github.com/gopherex/ratel/examples/shop/internal/db/app"
	"github.com/gopherex/ratel/examples/shop/internal/db/users"
)

//go:embed migrations
var migrations embed.FS

func main() {
	if err := run(context.Background(), os.Getenv("DATABASE_URL"), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// topCustomers is how many customers the report lists.
const topCustomers = 10

func run(ctx context.Context, url string, out io.Writer) error {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	// Apply the planned migrations: no table is locked longer than a moment.
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	if err := migrate.Apply(ctx, pool, dir); err != nil {
		return err
	}

	database := ratel.New(pool)

	// Insert, returning the generated rows.
	created, err := database.Run(app.Users.Insert(
		app.Users.Row().Email("ann@example.com").Name("Ann"),
		app.Users.Row().Email("bob@example.com").Name("Bob"),
	).Returning()).List(ctx)
	if err != nil {
		return err
	}

	ann := created[0]
	for _, total := range []string{"12.50", "40.00"} {
		order := app.Orders.Row().UserID(ann.ID).Total(decimal.RequireFromString(total))
		if _, err := database.Run(app.Orders.Insert(order)).Exec(ctx); err != nil {
			return err
		}
	}

	// Typed builder with an eager relation: two queries, no N+1.
	loaded, err := database.Run(app.Users.Select().
		Where(app.Users.Email.Eq("ann@example.com")).
		With(app.Users.Orders)).
		One(ctx)
	if err != nil {
		return err
	}

	orders, err := loaded.Orders()
	if err != nil {
		return err
	}

	fmt.Fprintln(out, loaded.Name, "has", len(orders), "orders")

	// A static query from queries/users/users.sql.
	top, err := database.Run(users.TopCustomers().Limit(topCustomers)).List(ctx)
	if err != nil {
		return err
	}

	for _, row := range top {
		fmt.Fprintln(out, row.Email, row.Spent.V)
	}

	return nil
}
