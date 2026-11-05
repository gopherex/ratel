-- One PostgreSQL schema, models split into packages by table prefix
-- (options.json): auth_* and people_* get their own package, the rest and the
-- schema's types stay in package public.
CREATE TYPE status AS ENUM ('active', 'blocked');

CREATE TABLE auth_users (
    id bigint PRIMARY KEY,
    status status NOT NULL
);

CREATE TABLE auth_sessions (
    id bigint PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES auth_users (id)
);

CREATE INDEX auth_sessions_user_id_idx ON auth_sessions (user_id);

CREATE TABLE people_profiles (
    id bigint PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES auth_users (id),
    name text NOT NULL
);

CREATE INDEX people_profiles_user_id_idx ON people_profiles (user_id);

CREATE TABLE settings (
    key text PRIMARY KEY,
    value text NOT NULL
);
