package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// PrefixCache is a disposable provider-resource registry, not a request/task log.
// Schema changes are applied through the explicit migration command only.
type PrefixCache struct {
	ent.Schema
}

func (PrefixCache) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Schema: "modelhub", Table: "prefix_cache"}}
}

func (PrefixCache) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("cache_key").Immutable(),
		field.String("cached_content"),
		field.Time("expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}
