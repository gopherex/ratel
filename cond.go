package ratel

// Cond2 retains both models of a join predicate and cannot be used in Where.
// Left and right source aliases are bound by the consuming join.
type Cond2[M, N any] struct {
	expr func(*sqlWriter, string, string)
}

// SQL renders a join predicate with independently quoted source aliases.
func (c Cond2[M, N]) SQL(leftAlias, rightAlias string) (Statement, error) {
	writer := &sqlWriter{}
	c.appendSQL(writer, leftAlias, rightAlias)

	if writer.invalid {
		return Statement{}, ErrInvalidQuery
	}

	return Statement{SQL: writer.String(), Args: writer.args}, nil
}

func (c Cond2[M, N]) appendSQL(writer *sqlWriter, leftAlias, rightAlias string) {
	if c.expr == nil {
		writer.WriteString("TRUE")

		return
	}

	c.expr(writer, leftAlias, rightAlias)
}

// And groups predicates involving the same pair of models.
func (c Cond2[M, N]) And(other Cond2[M, N]) Cond2[M, N] { return c.combine(" AND ", other) }

// Or groups predicates involving the same pair of models.
func (c Cond2[M, N]) Or(other Cond2[M, N]) Cond2[M, N] { return c.combine(" OR ", other) }

func (c Cond2[M, N]) combine(operator string, other Cond2[M, N]) Cond2[M, N] {
	return Cond2[M, N]{expr: func(writer *sqlWriter, leftAlias, rightAlias string) {
		writer.WriteByte('(')
		c.appendSQL(writer, leftAlias, rightAlias)
		writer.WriteString(operator)
		other.appendSQL(writer, leftAlias, rightAlias)
		writer.WriteByte(')')
	}}
}

// Not negates a grouped join predicate.
func (c Cond2[M, N]) Not() Cond2[M, N] {
	return Cond2[M, N]{expr: func(writer *sqlWriter, leftAlias, rightAlias string) {
		writer.WriteString("NOT (")
		c.appendSQL(writer, leftAlias, rightAlias)
		writer.WriteByte(')')
	}}
}

// Cond is a predicate belonging to one result model. Its zero value is TRUE.
type Cond[M any] struct{ expr func(*sqlWriter) }

func (c Cond[M]) appendSQL(writer *sqlWriter) {
	if c.expr == nil {
		writer.WriteString("TRUE")

		return
	}

	c.expr(writer)
}

// And groups two conditions using SQL AND.
func (c Cond[M]) And(other Cond[M]) Cond[M] { return c.combine(" AND ", other) }

// Or groups two conditions using SQL OR.
func (c Cond[M]) Or(other Cond[M]) Cond[M] { return c.combine(" OR ", other) }

func (c Cond[M]) combine(operator string, other Cond[M]) Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.WriteByte('(')
		c.appendSQL(writer)
		writer.WriteString(operator)
		other.appendSQL(writer)
		writer.WriteByte(')')
	}}
}

// Not negates a grouped condition.
func (c Cond[M]) Not() Cond[M] {
	return Cond[M]{expr: func(writer *sqlWriter) {
		writer.WriteString("NOT (")
		c.appendSQL(writer)
		writer.WriteByte(')')
	}}
}
