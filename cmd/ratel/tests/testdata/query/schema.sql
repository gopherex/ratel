CREATE TABLE public.users (
    id bigint PRIMARY KEY,
    name text NOT NULL,
    manager_id bigint,
    score integer NOT NULL
);
CREATE VIEW public.people AS SELECT id, name FROM public.users;
CREATE SCHEMA "x.y";
CREATE SCHEMA x;
CREATE FUNCTION "x.y".pick(value int4) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION x."y.pick"(value int4) RETURNS text LANGUAGE sql AS 'SELECT value::text';
CREATE FUNCTION public.widen(value bigint) RETURNS bigint
    LANGUAGE sql STRICT AS 'SELECT value';
CREATE TYPE public.status AS ENUM ('active');
CREATE FUNCTION public.identifiers(value bigint) RETURNS TABLE(id bigint)
    LANGUAGE sql AS 'SELECT value';
CREATE VIEW public.unqualified_people AS SELECT id FROM users;
CREATE FUNCTION public.tricky(value int4, extra int4 DEFAULT 0) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.tricky(value int4, extra text DEFAULT '') RETURNS text LANGUAGE sql AS 'SELECT extra';
CREATE FUNCTION public.with_default(value int4, extra int4 DEFAULT 0) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.collect(VARIADIC items int4[]) RETURNS int4 LANGUAGE sql AS 'SELECT 1';
CREATE TABLE public.arrays(id int4 PRIMARY KEY, items int4[] NOT NULL);
CREATE DOMAIN public.amount AS int4;
CREATE TABLE public.money(value public.amount NOT NULL);
CREATE FUNCTION public.choose_kind(value bool) RETURNS bool LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.choose_kind(value int4) RETURNS int4 LANGUAGE sql AS 'SELECT value';
CREATE FUNCTION public.user_rows() RETURNS SETOF public.users LANGUAGE sql AS 'SELECT * FROM public.users';
