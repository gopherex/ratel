// Command staticquery renders one static query result through text/template.
// Templates look up methods dynamically, as do dependencies in larger applications.
package main

import (
	"context"
	"log"
	"os"
	"text/template"

	"github.com/jackc/pgx/v5"

	"github.com/gopherex/ratel/pkg/ratel"
)

type result struct{ ID int64 }

func scan(row pgx.Row) (result, error) {
	var model result
	err := row.Scan(&model.ID)

	return model, err
}

func main() {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close(ctx)

	query := ratel.NewQuery([]string{"id"}, scan, "SELECT $1::bigint AS id", int64(1))
	row, err := ratel.New(conn).Run(query).First(ctx)
	if err != nil {
		log.Fatal(err)
	}

	if err := template.Must(template.New("row").Parse("{{.ID}}")).Execute(os.Stdout, row); err != nil {
		log.Fatal(err)
	}
}
