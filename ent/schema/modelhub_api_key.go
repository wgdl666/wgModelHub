package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ModelhubAPIKey 映射 modelhub.modelhub_api_key 公网 gRPC 鉴权用的 API Key 表；明文 secret 永不落库。
// 表结构由 migrations/002 维护，进程启动不做 DDL。
type ModelhubAPIKey struct {
	ent.Schema
}

func (ModelhubAPIKey) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{
		Schema: "modelhub",
		Table:  "modelhub_api_key",
	}}
}

func (ModelhubAPIKey) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			Immutable(),
		// principal_id 在轮换时保持不变，便于 caller=public:<principal_id> 连续归因。
		field.String("principal_id").
			Immutable(),
		field.String("key_id").
			Unique().
			Immutable(),
		field.String("secret_sha256"),
		field.String("name").
			Default(""),
		field.String("created_by").
			Immutable(),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// NULL 表示永不过期；显式未来时间仍按到期拒绝。不做 9999 年哨兵。
		field.Time("expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("revoked_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ModelhubAPIKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_by"),
		index.Fields("principal_id"),
	}
}
