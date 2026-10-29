package ratel

import (
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Codec converts a column's Go values to and from the values pgx sends and
// reads, for a column whose Go type is not its PostgreSQL value, such as a
// proto enum stored as its name.
type Codec[V any] struct {
	// Encode is the pgx argument of a value.
	Encode func(V) any
	// Scan reads the single column of the current row; NULL is an invalid Null.
	Scan func(pgx.Rows) (Null[V], error)
}

// NewCodecCol constructs a column whose values convert through codec.
func NewCodecCol[M, V any](name string, codec Codec[V]) Col[M, V] {
	return Col[M, V]{name: name, codec: &codec}
}

// NewCodecNullCol constructs a nullable column whose values convert through codec.
func NewCodecNullCol[M, V any](name string, codec Codec[V]) NullCol[M, V] {
	return NullCol[M, V]{Col: NewCodecCol[M](name, codec)}
}

// arg is a value as pgx receives it.
func (c Col[M, V]) arg(value V) any {
	if c.codec == nil {
		return value
	}

	return c.codec.Encode(value)
}

// scanValue reads a non-null column through the codec.
func (c *Codec[V]) scanValue(rows pgx.Rows) (V, error) {
	value, err := c.scanNull(rows)
	if err == nil && !value.Valid {
		err = ErrUnexpectedNull
	}

	return value.V, err
}

func (c *Codec[V]) scanNull(rows pgx.Rows) (Null[V], error) {
	value, err := c.Scan(rows)
	if err != nil {
		return Null[V]{}, fmt.Errorf("ratel: scan pluck: %w", err)
	}

	return value, nil
}
