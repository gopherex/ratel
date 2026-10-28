CREATE SCHEMA catalog_fixture;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TYPE catalog_fixture.status AS ENUM ('active', 'closed');
CREATE DOMAIN catalog_fixture.positive AS integer CHECK (VALUE > 0);
CREATE TYPE catalog_fixture.point AS (x integer, y integer);
CREATE TYPE catalog_fixture.interval AS RANGE (subtype = int4);
CREATE SEQUENCE catalog_fixture.ids START WITH 10 INCREMENT BY 2;
CREATE FUNCTION catalog_fixture.constant_value() RETURNS integer
  LANGUAGE sql IMMUTABLE AS 'SELECT 1';
CREATE PROCEDURE catalog_fixture.noop()
  LANGUAGE sql AS 'SELECT 1';
CREATE FUNCTION catalog_fixture.on_change() RETURNS trigger LANGUAGE plpgsql
  AS $$BEGIN RETURN NEW; END$$;
CREATE TABLE catalog_fixture.parents (
  id integer PRIMARY KEY,
  child_id integer,
  status catalog_fixture.status NOT NULL,
  amount catalog_fixture.positive,
  value integer DEFAULT catalog_fixture.constant_value(),
  sequence_value bigint DEFAULT nextval('catalog_fixture.ids'),
  doubled integer GENERATED ALWAYS AS (id * 2) STORED
);
CREATE TABLE catalog_fixture.children (
  id integer PRIMARY KEY,
  -- @ratel.rel name=Parent
  parent_id integer REFERENCES catalog_fixture.parents (id)
);
-- @ratel.rel name=Child
ALTER TABLE catalog_fixture.parents
  ADD CONSTRAINT parents_child FOREIGN KEY (child_id) REFERENCES catalog_fixture.children (id),
  ENABLE ROW LEVEL SECURITY,
  FORCE ROW LEVEL SECURITY;
CREATE UNIQUE INDEX parents_status ON catalog_fixture.parents (status);
CREATE POLICY visible ON catalog_fixture.parents USING (true);
CREATE TRIGGER changed BEFORE INSERT ON catalog_fixture.parents
  FOR EACH ROW EXECUTE FUNCTION catalog_fixture.on_change();
CREATE VIEW catalog_fixture.parent_ids AS SELECT id FROM catalog_fixture.parents;
CREATE MATERIALIZED VIEW catalog_fixture.child_ids AS SELECT id FROM catalog_fixture.children WITH NO DATA;
CREATE TABLE catalog_fixture.events (id integer) PARTITION BY RANGE (id);
CREATE TABLE catalog_fixture.events_first PARTITION OF catalog_fixture.events
  FOR VALUES FROM (0) TO (10);
COMMENT ON TABLE catalog_fixture.parents IS 'Parent records';
COMMENT ON COLUMN catalog_fixture.parents.id IS 'Primary identity';
GRANT SELECT ON catalog_fixture.parents TO PUBLIC;
COMMENT ON TYPE catalog_fixture.status IS 'Lifecycle state';
COMMENT ON DOMAIN catalog_fixture.positive IS 'Positive amount';
GRANT USAGE ON TYPE catalog_fixture.status TO PUBLIC;
GRANT USAGE ON DOMAIN catalog_fixture.positive TO PUBLIC;
