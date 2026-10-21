package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
)

// CompositeGroupTarget links a Composite group to a concrete target group.
type CompositeGroupTarget struct {
	ent.Schema
}

func (CompositeGroupTarget) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "composite_group_targets"},
	}
}

func (CompositeGroupTarget) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (CompositeGroupTarget) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("composite_group_id"),
		field.Int64("target_group_id"),
		field.Int("priority").Default(100),
		field.Bool("enabled").Default(true),
	}
}

func (CompositeGroupTarget) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("composite_group", Group.Type).
			Unique().
			Required().
			Field("composite_group_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("target_group", Group.Type).
			Unique().
			Required().
			Field("target_group_id").
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (CompositeGroupTarget) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("composite_group_id"),
		index.Fields("composite_group_id", "enabled", "priority"),
		index.Fields("target_group_id"),
		index.Fields("deleted_at"),
	}
}
