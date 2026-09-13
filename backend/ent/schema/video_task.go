package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// VideoTask stores asynchronous OpenAI-compatible video generation tasks.
type VideoTask struct {
	ent.Schema
}

func (VideoTask) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "video_tasks"},
	}
}

func (VideoTask) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (VideoTask) Fields() []ent.Field {
	return []ent.Field{
		field.String("public_task_id").
			MaxLen(80).
			NotEmpty(),
		field.String("upstream_task_id").
			MaxLen(160).
			Optional().
			Nillable(),

		field.String("provider").
			MaxLen(64).
			NotEmpty(),
		field.String("platform").
			MaxLen(64).
			NotEmpty(),
		field.Int64("user_id"),
		field.Int64("api_key_id"),
		field.Int64("group_id"),
		field.Int64("subscription_id").
			Optional().
			Nillable(),
		field.Int64("account_id"),
		field.Int64("channel_id").
			Optional().
			Nillable(),

		field.String("requested_model").
			MaxLen(200).
			NotEmpty(),
		field.String("upstream_model").
			MaxLen(200).
			NotEmpty(),
		field.String("billing_model").
			MaxLen(200).
			NotEmpty(),
		field.String("model_mapping_chain").
			MaxLen(500).
			Optional().
			Nillable(),

		field.String("status").
			MaxLen(32).
			Default("submitting"),
		field.String("provider_status").
			MaxLen(64).
			Optional().
			Nillable(),
		field.Int("progress").
			Default(0).
			Range(0, 100),

		field.String("prompt").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("request_hash").
			MaxLen(64).
			NotEmpty(),
		field.String("prompt_hash").
			MaxLen(64).
			Optional().
			Nillable(),
		field.Bytes("request_body").
			Optional().
			Nillable(),
		field.JSON("request_metadata", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		field.String("upstream_base_url").
			MaxLen(500).
			Optional().
			Nillable(),
		field.JSON("upstream_response", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Bytes("upstream_response_body").
			Optional().
			Nillable(),

		field.String("result_url").
			MaxLen(1000).
			Optional().
			Nillable(),
		field.String("result_content_type").
			MaxLen(100).
			Optional().
			Nillable(),
		field.JSON("result_metadata", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("error_code").
			MaxLen(128).
			Optional().
			Nillable(),
		field.String("error_message").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("idempotency_key").
			MaxLen(255).
			Optional().
			Nillable(),
		field.String("idempotency_key_hash").
			MaxLen(64).
			Optional().
			Nillable(),
		field.JSON("usage_metadata", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("usage_log_id").
			Optional().
			Nillable(),
		field.Int("input_tokens").
			Default(0),
		field.Int("output_tokens").
			Default(0),
		field.Float("billed_usd").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),

		field.Time("submitted_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("started_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("next_poll_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_polled_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("locked_until").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("locked_by").
			MaxLen(128).
			Optional().
			Nillable(),
		field.Int("poll_attempts").
			Default(0),
		field.Time("user_deleted_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (VideoTask) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("public_task_id").Unique(),
		index.Fields("api_key_id", "idempotency_key").
			Unique().
			Annotations(entsql.IndexWhere("idempotency_key IS NOT NULL")),
		index.Fields("user_id", "created_at"),
		index.Fields("account_id", "status"),
		index.Fields("status", "next_poll_at"),
		index.Fields("request_hash"),
		index.Fields("user_deleted_at"),
	}
}
