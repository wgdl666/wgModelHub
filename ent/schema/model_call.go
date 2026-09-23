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

// ModelCall 映射 modelhub.model_call 调用账本；只存真实供应商生成调用，不做启动 DDL。
type ModelCall struct {
	ent.Schema
}

func (ModelCall) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{
		Schema: "modelhub",
		Table:  "model_call",
	}}
}

func (ModelCall) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").
			StorageKey("call_id").
			Immutable(),
		// 异步视频幂等键；同步调用保持 NULL，与 PG UNIQUE 多 NULL 语义一致。
		field.String("generation_task_id").
			Optional().
			Nillable().
			Unique(),
		field.String("caller_service").
			Default("unknown"),
		field.String("business_scene").
			Default("unknown"),
		field.String("operation"),
		field.String("capability"),
		field.String("model"),
		field.String("provider"),
		field.String("status"),
		field.String("delivery_status").
			Default("ok"),
		field.String("error_category").
			Default(""),
		field.String("error_code").
			Default(""),
		field.String("error_reason").
			Default(""),
		field.String("error_message").
			Default(""),
		field.Int64("input_tokens").
			Optional().
			Nillable(),
		field.Int64("output_tokens").
			Optional().
			Nillable(),
		field.Int64("total_tokens").
			Optional().
			Nillable(),
		field.Int64("cached_tokens").
			Optional().
			Nillable(),
		field.Int64("reasoning_tokens").
			Optional().
			Nillable(),
		field.Int("image_count").
			Optional().
			Nillable(),
		field.String("image_size").
			Default(""),
		field.String("image_aspect_ratio").
			Default(""),
		field.Int("video_count").
			Optional().
			Nillable(),
		field.String("video_resolution").
			Default(""),
		field.Int("video_duration_seconds").
			Optional().
			Nillable(),
		field.String("video_aspect_ratio").
			Default(""),
		field.Int64("latency_ms").
			Default(0),
		field.Time("started_at").
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("finished_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.JSON("input_payload", map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Default(map[string]any{}),
		field.JSON("output_payload", map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Default(map[string]any{}),
		field.JSON("usage_detail", map[string]any{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Default(map[string]any{}),
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

func (ModelCall) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("started_at"),
		index.Fields("caller_service", "started_at"),
		index.Fields("business_scene", "started_at"),
		index.Fields("model", "started_at"),
		index.Fields("status", "started_at"),
	}
}
