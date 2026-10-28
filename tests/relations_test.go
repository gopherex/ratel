//go:build integration

package tests_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gopherex/ratel"
)

type relUser struct {
	Tenant    int64
	ID        int64
	ManagerID ratel.Null[int64]
	R         relUserRelations
}

type relUserRelations struct {
	Orders     ratel.Loaded[[]relOrder]
	Tags       ratel.Loaded[[]relTag]
	Manager    ratel.Loaded[*relUser]
	OrderCount ratel.Loaded[int64]
	Peers      ratel.Loaded[[]relUser]
}

func (u *relUser) Orders() ([]relOrder, error) { return u.R.Orders.Get() }
func (u *relUser) Manager() (*relUser, error)  { return u.R.Manager.Get() }

type relOrder struct {
	Tenant int64
	ID     int64
	UserID int64
	R      relOrderRelations
}

type relOrderRelations struct{ Items ratel.Loaded[[]relItem] }

type relItem struct {
	Tenant  int64
	ID      int64
	OrderID int64
}
type relTag struct {
	ID   int64
	Name string
}

type relUsers struct {
	ratel.Table[relUser]
	Tenant  ratel.Col[relUser, int64]
	ID      ratel.Col[relUser, int64]
	Orders  ratel.Rel[relUser, relOrder]
	Tags    ratel.Rel[relUser, relTag]
	Manager ratel.Rel[relUser, relUser]
}

type relOrders struct {
	ratel.Table[relOrder]
	ID    ratel.Col[relOrder, int64]
	Items ratel.Rel[relOrder, relItem]
}

func relUserTable() ratel.Table[relUser] {
	return ratel.NewTable("rel_app", "users", []string{"tenant", "id", "manager_id"},
		func(row pgx.Row) (relUser, error) {
			var model relUser
			err := row.Scan(&model.Tenant, &model.ID, &model.ManagerID)

			return model, err
		}, ratel.PrimaryKey([]string{"tenant", "id"}, func(model relUser) []any {
			return []any{model.Tenant, model.ID}
		}))
}

func relOrderTable() ratel.Table[relOrder] {
	return ratel.NewTable("rel_app", "orders", []string{"tenant", "id", "user_id"},
		func(row pgx.Row) (relOrder, error) {
			var model relOrder
			err := row.Scan(&model.Tenant, &model.ID, &model.UserID)

			return model, err
		}, ratel.PrimaryKey([]string{"tenant", "id"}, func(model relOrder) []any {
			return []any{model.Tenant, model.ID}
		}))
}

func relUserKeys(projectID func(relUser) int64) []ratel.RelKey[relUser] {
	return []ratel.RelKey[relUser]{
		{Column: "tenant", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(models []relUser) any {
			values := make([]int64, len(models))
			for index := range models {
				values[index] = models[index].Tenant
			}

			return values
		}},
		{Column: "user_id", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(models []relUser) any {
			values := make([]int64, len(models))
			for index := range models {
				values[index] = projectID(models[index])
			}

			return values
		}},
	}
}

func relDescriptors() (relUsers, relOrders) {
	orders := relOrders{Table: relOrderTable(), ID: ratel.NewCol[relOrder, int64]("id")}
	users := relUsers{
		Table: relUserTable(), Tenant: ratel.NewCol[relUser, int64]("tenant"),
		ID: ratel.NewCol[relUser, int64]("id"),
	}
	users.Orders = ratel.NewRel(orders, ratel.RelMany, relUserKeys(func(model relUser) int64 { return model.ID }),
		func(model *relUser, children []relOrder) { model.R.Orders = ratel.LoadedFrom(children) },
		ratel.RelCount[relUser, relOrder](func(model *relUser, count int64) { model.R.OrderCount = ratel.LoadedFrom(count) }))
	managerKeys := relUserKeys(func(model relUser) int64 { return model.ID })
	managerKeys[1].Column = "id"
	managerKeys[1].Values = func(models []relUser) any {
		values := make([]pgtype.Int8, len(models))
		for index := range models {
			values[index] = pgtype.Int8{Int64: models[index].ManagerID.V, Valid: models[index].ManagerID.Valid}
		}

		return values
	}
	users.Manager = ratel.NewRel(users, ratel.RelOne, managerKeys, func(model *relUser, children []relUser) {
		var manager *relUser
		if len(children) > 0 {
			manager = &children[0]
		}

		model.R.Manager = ratel.LoadedFrom(manager)
	})
	tags := ratel.NewTable("rel_app", "tags", []string{"id", "name"}, func(row pgx.Row) (relTag, error) {
		var model relTag
		err := row.Scan(&model.ID, &model.Name)

		return model, err
	}, ratel.PrimaryKey([]string{"id"}, func(model relTag) []any { return []any{model.ID} }))
	users.Tags = ratel.NewRel(tags, ratel.RelMany, relUserKeys(func(model relUser) int64 { return model.ID }),
		func(model *relUser, children []relTag) { model.R.Tags = ratel.LoadedFrom(children) },
		ratel.Through[relUser, relTag]("rel_app", "user_tags", "tag_id"))
	orders.Items = relItems()

	return users, orders
}

func relItems() ratel.Rel[relOrder, relItem] {
	items := ratel.NewTable("rel_app", "items", []string{"tenant", "id", "order_id"},
		func(row pgx.Row) (relItem, error) {
			var model relItem
			err := row.Scan(&model.Tenant, &model.ID, &model.OrderID)

			return model, err
		}, ratel.PrimaryKey([]string{"tenant", "id"}, func(model relItem) []any {
			return []any{model.Tenant, model.ID}
		}))
	keys := []ratel.RelKey[relOrder]{
		{Column: "tenant", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(models []relOrder) any {
			values := make([]int64, len(models))
			for index := range models {
				values[index] = models[index].Tenant
			}

			return values
		}},
		{Column: "order_id", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(models []relOrder) any {
			values := make([]int64, len(models))
			for index := range models {
				values[index] = models[index].ID
			}

			return values
		}},
	}

	return ratel.NewRel(items, ratel.RelMany, keys,
		func(model *relOrder, children []relItem) { model.R.Items = ratel.LoadedFrom(children) })
}

func relationPool(tb testing.TB) *pgxpool.Pool {
	tb.Helper()
	pool := valuePool(tb)

	_, err := pool.Exec(tb.Context(), `CREATE SCHEMA rel_app;
 CREATE TABLE rel_app.users (tenant bigint NOT NULL, id bigint NOT NULL, manager_id bigint,
 PRIMARY KEY (tenant,id), FOREIGN KEY (tenant,manager_id) REFERENCES rel_app.users);
 CREATE TABLE rel_app.orders (tenant bigint NOT NULL, id bigint NOT NULL, user_id bigint NOT NULL,
 PRIMARY KEY (tenant,id), FOREIGN KEY (tenant,user_id) REFERENCES rel_app.users);
 CREATE TABLE rel_app.items (tenant bigint NOT NULL, id bigint NOT NULL, order_id bigint NOT NULL,
 PRIMARY KEY (tenant,id), FOREIGN KEY (tenant,order_id) REFERENCES rel_app.orders);
 CREATE TABLE rel_app.tags (id bigint PRIMARY KEY, name text NOT NULL);
 CREATE TABLE rel_app.user_tags (tenant bigint NOT NULL, user_id bigint NOT NULL, tag_id bigint NOT NULL,
 PRIMARY KEY (tenant,user_id,tag_id), FOREIGN KEY (tenant,user_id) REFERENCES rel_app.users,
 FOREIGN KEY (tag_id) REFERENCES rel_app.tags);
 CREATE TABLE rel_app.user_peers (tenant bigint NOT NULL, user_id bigint NOT NULL,
 peer_tenant bigint NOT NULL, peer_id bigint NOT NULL,
 PRIMARY KEY (tenant,user_id,peer_tenant,peer_id),
 FOREIGN KEY (tenant,user_id) REFERENCES rel_app.users,
 FOREIGN KEY (peer_tenant,peer_id) REFERENCES rel_app.users);
 INSERT INTO rel_app.users VALUES (1,1,NULL),(1,2,1),(1,3,NULL),(2,1,NULL);
 INSERT INTO rel_app.orders VALUES (1,11,1),(1,12,1),(1,13,1),(1,21,2),(2,11,1);
 INSERT INTO rel_app.items VALUES (1,111,11),(1,112,11),(1,121,12),(2,111,11);
 INSERT INTO rel_app.tags VALUES (10,'red'),(11,'blue');
 INSERT INTO rel_app.user_tags VALUES (1,1,10),(1,1,11),(1,2,11),(2,1,10);
 INSERT INTO rel_app.user_peers VALUES (1,1,1,2),(1,1,2,1);`)
	if err != nil {
		tb.Fatal(err)
	}

	return pool
}

type relationCounter struct {
	ratel.Executor
	batches []int
	queries int
	kill    bool
}

func (e *relationCounter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	e.queries++

	return e.Executor.Query(ctx, sql, args...)
}

func (e *relationCounter) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	e.batches = append(e.batches, batch.Len())
	if e.kill {
		e.kill = false
		injected := &pgx.Batch{}

		for index, query := range batch.QueuedQueries {
			sql := query.SQL
			if index == 1 {
				sql = "SELECT * FROM (" + sql + ") AS victim " +
					"WHERE pg_catalog.pg_terminate_backend(pg_catalog.pg_backend_pid())"
			}

			injected.Queue(sql, query.Arguments...)
		}

		return e.Executor.SendBatch(ctx, injected)
	}

	return e.Executor.SendBatch(ctx, batch)
}

func TestRelationsRuntime(t *testing.T) {
	t.Parallel()
	pool := relationPool(t)

	for _, tc := range []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool)
	}{
		{"nested composite graph", relationsNested},
		{"per-parent strategies", relationsPagination},
		{"static and duplicate parents", relationsStatic},
		{"chunks", relationsChunks},
		{"connection protocols", relationsProtocols},
		{"single key ANY", relationsSingleKey},
		{"composite join table target", relationsCompositeThrough},
		{"connection kill mid-batch", relationsChaos},
	} {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); tc.run(t, pool) })
	}
}

func relationsNested(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	users, orders := relDescriptors()
	counter := &relationCounter{Executor: pool}

	models, err := ratel.New(counter).From(users).With(users.Orders.OrderBy(orders.ID.Asc()).With(orders.Items),
		users.Tags, users.Manager).WithCount(users.Orders).OrderBy(users.Tenant.Asc(), users.ID.Asc()).List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(models) != 4 || !reflect.DeepEqual(counter.batches, []int{4, 1}) || counter.queries != 1 {
		t.Fatalf("models=%d queries=%d batches=%v", len(models), counter.queries, counter.batches)
	}

	assertRelChildren(t, models)
	assertRelDetails(t, models)
}

func assertRelChildren(t *testing.T, models []relUser) {
	t.Helper()

	for index, expected := range []int{3, 1, 0, 1} {
		children, failure := models[index].Orders()
		count, countFailure := models[index].R.OrderCount.Get()

		if failure != nil || countFailure != nil || len(children) != expected || count != int64(expected) {
			t.Fatalf("parent %d children=%v count=%d errors=%v,%v", index, children, count, failure, countFailure)
		}

		for _, child := range children {
			if child.Tenant != models[index].Tenant {
				t.Fatal("cross-tenant attachment")
			}

			if _, err := child.R.Items.Get(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func assertRelDetails(t *testing.T, models []relUser) {
	t.Helper()

	items, err := models[0].R.Orders.Get()
	if err != nil {
		t.Fatal(err)
	}

	nested, err := items[0].R.Items.Get()
	if err != nil || len(nested) != 2 {
		t.Fatalf("items=%v err=%v", nested, err)
	}

	manager, err := models[1].Manager()
	if err != nil || manager == nil || manager.ID != 1 || manager.Tenant != 1 {
		t.Fatalf("manager=%v err=%v", manager, err)
	}

	manager, err = models[0].Manager()
	if err != nil || manager != nil {
		t.Fatalf("empty manager=%v err=%v", manager, err)
	}

	tags, err := models[0].R.Tags.Get()
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags=%v err=%v", tags, err)
	}
}

func relationsPagination(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	users, orders := relDescriptors()

	for _, strategy := range []ratel.LoadStrategy{{}, ratel.Window()} {
		counter := &relationCounter{Executor: pool}

		models, err := ratel.New(counter).From(users).Where(users.Tenant.Eq(1)).OrderBy(users.ID.Asc()).
			With(users.Orders.OrderBy(orders.ID.Desc()).Offset(1).Limit(1).Strategy(strategy),
				users.Tags.Limit(1), users.Manager.Strategy(ratel.Joined())).
			WithCount(users.Orders.Strategy(ratel.Correlated())).List(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		children, err := models[0].Orders()
		if err != nil || len(children) != 1 || children[0].ID != 12 {
			t.Fatalf("children=%v err=%v", children, err)
		}

		empty, err := models[1].Orders()
		if err != nil || len(empty) != 0 {
			t.Fatalf("empty=%v err=%v", empty, err)
		}

		count, err := models[0].R.OrderCount.Get()
		if err != nil || count != 3 || !reflect.DeepEqual(counter.batches, []int{4}) {
			t.Fatalf("count=%d batches=%v err=%v", count, counter.batches, err)
		}
	}
}

func relationsStatic(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	users, _ := relDescriptors()
	source := ratel.NewQuery([]string{"tenant", "id", "manager_id"}, func(row pgx.Row) (relUser, error) {
		var model relUser
		err := row.Scan(&model.Tenant, &model.ID, &model.ManagerID)

		return model, err
	}, `SELECT tenant,id,manager_id FROM rel_app.users WHERE tenant=1 AND id=1
 UNION ALL SELECT tenant,id,manager_id FROM rel_app.users WHERE tenant=1 AND id=1`)
	counter := &relationCounter{Executor: pool}
	database := ratel.New(counter)

	models, err := database.From(source).List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if _, failure := models[0].Orders(); !errors.Is(failure, ratel.ErrNotLoaded) {
		t.Fatal(failure)
	}

	if err := database.Load(t.Context(), models, users.Orders); err != nil {
		t.Fatal(err)
	}

	for index := range models {
		children, failure := models[index].Orders()
		if failure != nil || len(children) != 3 {
			t.Fatalf("children=%v err=%v", children, failure)
		}
	}

	if err := database.Load(t.Context(), []relUser{}, users.Orders); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(counter.batches, []int{1}) {
		t.Fatal(counter.batches)
	}
}

func relationsChunks(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	users, _ := relDescriptors()
	counter := &relationCounter{Executor: pool}
	query := ratel.New(counter).From(users).With(users.Orders).Limit(4)
	count := 0

	for page, err := range query.Chunks(t.Context(), 2) {
		if err != nil {
			t.Fatal(err)
		}

		for index := range page {
			if _, failure := page[index].Orders(); failure != nil {
				t.Fatal(failure)
			}

			count++
		}
	}

	if count != 4 || !reflect.DeepEqual(counter.batches, []int{1, 1}) || counter.queries != 2 {
		t.Fatalf("count=%d batches=%v queries=%d", count, counter.batches, counter.queries)
	}
}

func relationsProtocols(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	for _, mode := range []pgx.QueryExecMode{
		pgx.QueryExecModeCacheStatement, pgx.QueryExecModeCacheDescribe,
		pgx.QueryExecModeDescribeExec, pgx.QueryExecModeExec, pgx.QueryExecModeSimpleProtocol,
	} {
		relationsProtocol(t, pool, mode)
	}
}

func relationsProtocol(t *testing.T, pool *pgxpool.Pool, mode pgx.QueryExecMode) {
	t.Helper()

	config := pool.Config().ConnConfig.Copy()
	config.DefaultQueryExecMode = mode

	connection, err := pgx.ConnectConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}

	defer connection.Close(t.Context())

	if err := ratel.RegisterValue[int64](connection.TypeMap(), "int8"); err != nil {
		t.Fatal(err)
	}

	users, orders := relDescriptors()

	models, err := ratel.New(connection).From(users).Where(users.Tenant.Eq(1)).Where(users.ID.Eq(1)).
		With(users.Orders.Where(orders.ID.Lt(13)).OrderBy(orders.ID.Desc()).Limit(1)).
		WithCount(users.Orders.Where(orders.ID.Lt(13))).List(t.Context())
	if err != nil {
		t.Fatalf("%s: %v", mode, err)
	}

	children, err := models[0].Orders()
	if err != nil || len(children) != 1 || children[0].ID != 12 {
		t.Fatalf("%s: children=%v err=%v", mode, children, err)
	}

	count, err := models[0].R.OrderCount.Get()
	if err != nil || count != 2 {
		t.Fatalf("%s: count=%d err=%v", mode, count, err)
	}
}

func relationsSingleKey(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	users, _ := relDescriptors()
	keys := []ratel.RelKey[relUser]{
		{Column: "id", Type: pgx.Identifier{"pg_catalog", "int8"}, Values: func(models []relUser) any {
			values := make([]int64, len(models))
			for index := range models {
				values[index] = models[index].ID
			}

			return values
		}},
	}
	relation := ratel.NewRel(users, ratel.RelOne, keys, func(model *relUser, children []relUser) {
		var manager *relUser
		if len(children) != 0 {
			manager = &children[0]
		}
		model.R.Manager = ratel.LoadedFrom(manager)
	}).Where(users.Tenant.Eq(1))
	models := []relUser{{ID: 1}, {ID: 99}}

	if err := ratel.New(pool).Load(t.Context(), models, relation); err != nil {
		t.Fatal(err)
	}

	manager, err := models[0].Manager()
	if err != nil || manager == nil || manager.ID != 1 {
		t.Fatalf("manager=%v err=%v", manager, err)
	}

	manager, err = models[1].Manager()
	if err != nil || manager != nil {
		t.Fatalf("manager=%v err=%v", manager, err)
	}
}

func relationsCompositeThrough(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	users, _ := relDescriptors()
	peers := ratel.NewRel(users, ratel.RelMany, relUserKeys(func(model relUser) int64 { return model.ID }),
		func(model *relUser, children []relUser) { model.R.Peers = ratel.LoadedFrom(children) },
		ratel.Through[relUser, relUser]("rel_app", "user_peers", "peer_tenant", "peer_id"))
	models := []relUser{{Tenant: 1, ID: 1}, {Tenant: 1, ID: 3}}

	if err := ratel.New(pool).Load(t.Context(), models, peers.OrderBy(users.Tenant.Asc())); err != nil {
		t.Fatal(err)
	}

	loaded, err := models[0].R.Peers.Get()
	if err != nil || len(loaded) != 2 || loaded[0].Tenant != 1 || loaded[0].ID != 2 || loaded[1].Tenant != 2 {
		t.Fatalf("peers=%v err=%v", loaded, err)
	}

	empty, err := models[1].R.Peers.Get()
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("peers=%v err=%v", empty, err)
	}
}

func BenchmarkRelationLoad(b *testing.B) {
	pool := relationPool(b)
	users, orders := relDescriptors()
	relation := users.Orders.OrderBy(orders.ID.Asc())
	database := ratel.New(pool)

	for _, name := range []string{"generated", "handwritten"} {
		b.Run(name, func(b *testing.B) {
			models := []relUser{{Tenant: 1, ID: 1}, {Tenant: 1, ID: 2}, {Tenant: 1, ID: 3}, {Tenant: 2, ID: 1}}

			b.ReportAllocs()

			for b.Loop() {
				var err error
				if name == "generated" {
					err = database.Load(b.Context(), models, relation)
				} else {
					err = handwrittenRelationLoad(b.Context(), pool, models)
				}

				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func handwrittenRelationLoad(ctx context.Context, executor ratel.Executor, models []relUser) error {
	tenants := make([]int64, len(models))
	ids := make([]int64, len(models))

	for index := range models {
		tenants[index], ids[index] = models[index].Tenant, models[index].ID
	}

	batch := &pgx.Batch{}
	batch.Queue(`SELECT p.ordinal,q.tenant,q.id,q.user_id
 FROM unnest($1::pg_catalog.int8[],$2::pg_catalog.int8[]) WITH ORDINALITY AS p(tenant,id,ordinal)
 JOIN rel_app.orders AS q ON q.tenant=p.tenant AND q.user_id=p.id
 ORDER BY p.ordinal,q.id ASC`, tenants, ids)

	results := executor.SendBatch(ctx, batch)
	rows, err := results.Query()
	if err != nil {
		return errors.Join(err, results.Close())
	}

	groups := make([][]relOrder, len(models))
	for index := range groups {
		groups[index] = make([]relOrder, 0)
	}

	for rows.Next() {
		var parent int

		var model relOrder
		if err := rows.Scan(&parent, &model.Tenant, &model.ID, &model.UserID); err != nil {
			rows.Close()

			return errors.Join(err, results.Close())
		}

		groups[parent-1] = append(groups[parent-1], model)
	}

	rows.Close()

	if err := errors.Join(rows.Err(), results.Close()); err != nil {
		return err
	}

	for index := range models {
		models[index].R.Orders = ratel.LoadedFrom(groups[index])
	}

	return nil
}

func relationsChaos(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	users, _ := relDescriptors()
	counter := &relationCounter{Executor: pool, kill: true}
	database := ratel.New(counter)
	models := []relUser{{Tenant: 1, ID: 1}}
	err := database.Load(t.Context(), models, users.Orders, users.Tags)

	var serverError *pgconn.PgError

	if !errors.As(err, &serverError) || serverError.Code != "57P01" {
		t.Fatalf("lost connection cause: %v", err)
	}

	if _, failure := models[0].Orders(); !errors.Is(failure, ratel.ErrNotLoaded) {
		t.Fatal("partial attachment")
	}

	if err := database.Load(t.Context(), models, users.Orders, users.Tags); err != nil {
		t.Fatal(err)
	}

	orders, err := models[0].Orders()
	if err != nil || len(orders) != 3 {
		t.Fatalf("recovery orders=%v err=%v", orders, err)
	}
}
