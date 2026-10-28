package migrate

// Split exposes split to tests.
func Split(sql string) []Piece { return split(sql) }
