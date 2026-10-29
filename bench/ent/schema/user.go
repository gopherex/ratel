// Package schema declares the ent schema of the benchmark tables, matching
// schema/schema.sql.
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// User is public.users.
type User struct{ ent.Schema }

// Fields of User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("email").Unique(),
		field.String("name"),
		field.Time("created_at").Immutable().Optional(),
	}
}

// Edges of User.
func (User) Edges() []ent.Edge {
	return []ent.Edge{edge.To("orders", Order.Type)}
}

// Order is public.orders.
type Order struct{ ent.Schema }

// Fields of Order.
func (Order) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("user_id"),
		field.Int64("total"),
		field.String("status"),
	}
}

// Edges of Order.
func (Order) Edges() []ent.Edge {
	return []ent.Edge{edge.From("user", User.Type).Ref("orders").Field("user_id").Unique().Required()}
}
