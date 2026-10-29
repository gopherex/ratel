-- ratel:step id=e1 phase=expand tx=in class=short locks=app.t:AccessExclusive pg=16 ops=add-column:app.t::false
ALTER TABLE app.t ADD COLUMN email text;

-- ratel:step id=e2 phase=backfill tx=in class=backfill locks=app.t:RowExclusive pg=16 ops=backfill:app.t::false key=id
UPDATE app.t SET email = 'user' || id || '@example.org' WHERE email IS NULL;
