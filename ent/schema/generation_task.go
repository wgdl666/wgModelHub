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

// GenerationTask 映射已上线的 generation_task 表；列名/唯一键须与 migrations/001 及存量数据兼容。
// 运行时禁止 Schema.Create；表仍由显式 migration 维护。
type GenerationTask struct {
	ent.Schema
}

func (GenerationTask) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "generation_task"}}
}

func (GenerationTask) Fields() []ent.Field {
	return []ent.Field{
		// 业务主键是 task_id；Ent 的 id 字段用 StorageKey 对齐现有列，避免改表。
		field.String("id").
			StorageKey("task_id").
			Immutable(),
		field.String("caller"),
		field.String("request_id"),
		field.String("request_hash"),
		field.String("model"),
		field.String("provider"),
		field.String("provider_task_id").Default(""),
		field.String("state"),
		field.Int32("error_code").Default(0),
		field.String("error_message").Default(""),
		field.String("error_reason").Default(""),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (GenerationTask) Indexes() []ent.Index {
	// 幂等键 (caller, request_id)：插入冲突后按此键回查并比对 request_hash。
	return []ent.Index{
		index.Fields("caller", "request_id").Unique(),
	}
}
