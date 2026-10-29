// Command proto is the ratel quick start with proto messages as models: the
// schema comes from proto/ through protoc-gen-ratel, and rows are read into
// and written from the messages themselves.
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

	"github.com/gopherex/ratel"
	"github.com/gopherex/ratel/migrate"

	shopv1 "github.com/gopherex/ratel/examples/proto/gen/shop/v1"
	"github.com/gopherex/ratel/examples/proto/internal/db/shop"
)

//go:embed migrations
var migrations embed.FS

func main() {
	if err := run(context.Background(), os.Getenv("DATABASE_URL"), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

// The example's customer and order.
const (
	annID       = 1
	annTenant   = 7
	annSubtotal = 50
	orderID     = 10
)

func run(ctx context.Context, url string, out io.Writer) error {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	if err := migrate.Apply(ctx, pool, dir); err != nil {
		return err
	}

	database := ratel.New(pool)

	// Insert straight from a message: every column comes from its field.
	nickname := "ann"
	ann := &shopv1.Customer{
		Id: annID, TenantNo: annTenant, Email: "ann@example.com", Nickname: &nickname, Status: shopv1.Status_STATUS_ACTIVE,
		Address:  &shopv1.Address{Street: "Main 1", City: "Riga"},
		Contact:  &shopv1.Customer_Phone{Phone: "+371 2000 0000"},
		Subtotal: annSubtotal,
	}

	if _, err := database.Run(shop.Customers.Insert(shop.Customers.RowOf(ann))).Exec(ctx); err != nil {
		return err
	}

	order := shop.Orders.Row().ID(orderID).CustomerID(annID).Kind(shopv1.OrderKind_ORDER_KIND_WEB).
		History([]shopv1.OrderKind{shopv1.OrderKind_ORDER_KIND_WEB})
	if _, err := database.Run(shop.Orders.Insert(order)).Exec(ctx); err != nil {
		return err
	}

	// Conditions take proto values; the row is the message, with its relations.
	loaded, err := database.Run(shop.Customers.Select().
		Where(shop.Customers.Status.Eq(shopv1.Status_STATUS_ACTIVE)).
		With(shop.Customers.Orders)).One(ctx)
	if err != nil {
		return err
	}

	orders, err := loaded.Orders()
	if err != nil {
		return err
	}

	fmt.Fprintln(out, loaded.GetEmail(), loaded.GetAddress().GetCity(), loaded.GetPhone(), loaded.GetTotal(), len(orders))

	// Change the message and save it back by its primary key.
	loaded.Status = shopv1.Status_STATUS_CLOSED
	if _, err := database.Run(shop.Customers.Save(loaded)).Exec(ctx); err != nil {
		return err
	}

	statuses, err := database.Run(shop.Customers.Select().Pluck(shop.Customers.Status)).List(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, statuses[0])

	return nil
}
