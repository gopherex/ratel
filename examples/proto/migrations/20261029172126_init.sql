-- ratel:step id=eaf04dee phase=expand tx=in class=short pg=18 ops=create-schema:::false
CREATE SCHEMA shop;

-- ratel:step id=8076f304 phase=expand tx=in class=short pg=18 ops=create-type:::false
CREATE TYPE shop.order_kind AS ENUM ('ORDER_KIND_UNSPECIFIED', 'ORDER_KIND_WEB', 'ORDER_KIND_PHONE');

-- ratel:step id=64d37bf2 phase=expand tx=in class=short pg=18 ops=create-table:shop.customers::false
CREATE TABLE shop.customers (id bigint CONSTRAINT customers_id_not_null NOT NULL, tenant_no bigint CONSTRAINT customers_tenant_no_not_null NOT NULL, email text CONSTRAINT customers_email_not_null NOT NULL, nickname text, status text DEFAULT 'STATUS_ACTIVE'::text CONSTRAINT customers_status_not_null NOT NULL, created_at timestamp with time zone DEFAULT now(), ttl interval, score integer, tags text[] CONSTRAINT customers_tags_not_null NOT NULL, counters jsonb CONSTRAINT customers_counters_not_null NOT NULL, home_street text, home_city text, balance numeric(20,0) CONSTRAINT customers_balance_not_null NOT NULL, meta jsonb, notes jsonb CONSTRAINT customers_notes_not_null NOT NULL, pinned jsonb CONSTRAINT customers_pinned_not_null NOT NULL, subtotal bigint CONSTRAINT customers_subtotal_not_null NOT NULL, total bigint GENERATED ALWAYS AS ((subtotal * 2)) STORED CONSTRAINT customers_total_not_null NOT NULL, phone text, fax text, contact_case text);

-- ratel:step id=3c8f29fe phase=expand tx=in class=short pg=18 ops=create-table:shop.orders::false
CREATE TABLE shop.orders (id bigint CONSTRAINT orders_id_not_null NOT NULL, customer_id bigint CONSTRAINT orders_customer_id_not_null NOT NULL, kind shop.order_kind CONSTRAINT orders_kind_not_null NOT NULL, history shop.order_kind[] CONSTRAINT orders_history_not_null NOT NULL);

-- ratel:step id=bf01296d phase=expand tx=in class=short pg=18 ops=add-check:shop.customers::false
ALTER TABLE shop.customers ADD CONSTRAINT customers_contact_case_check CHECK ((contact_case = ANY (ARRAY['phone'::text, 'fax'::text])));

-- ratel:step id=20121f4f phase=expand tx=in class=short pg=18 ops=add-unique:shop.customers::false
ALTER TABLE shop.customers ADD CONSTRAINT customers_email_key UNIQUE (email);

-- ratel:step id=d4442433 phase=expand tx=in class=short pg=18 ops=add-unique:shop.customers::false
ALTER TABLE shop.customers ADD CONSTRAINT customers_pkey PRIMARY KEY (id);

-- ratel:step id=85f7eada phase=expand tx=in class=short pg=18 ops=add-check:shop.customers::false
ALTER TABLE shop.customers ADD CONSTRAINT customers_status_check CHECK ((status = ANY (ARRAY['STATUS_UNSPECIFIED'::text, 'STATUS_ACTIVE'::text, 'STATUS_CLOSED'::text])));

-- ratel:step id=302a6c54 phase=expand tx=in class=short pg=18 ops=add-unique:shop.orders::false
ALTER TABLE shop.orders ADD CONSTRAINT orders_pkey PRIMARY KEY (id);

-- ratel:step id=f8ccbe3f phase=expand tx=in class=short pg=18 ops=create-index:shop.orders::false
CREATE INDEX orders_customer_id_idx ON shop.orders USING btree (customer_id);

-- ratel:step id=448313c7 phase=expand tx=in class=short pg=18 ops=add-foreign-key:shop.orders:shop.customers:false
ALTER TABLE shop.orders ADD CONSTRAINT orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES shop.customers(id);
