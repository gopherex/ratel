-- The application no longer reads w.
-- ratel:step id=d1 phase=contract tx=in class=short by=hand locks=app.t:AccessExclusive pg=16 ops=drop-column:app.t::false
ALTER TABLE app.t DROP COLUMN w;
