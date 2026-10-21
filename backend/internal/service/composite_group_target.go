package service

import (
	"context"
	"time"
)

type CompositeGroupTarget struct {
	ID               int64      `json:"id"`
	CompositeGroupID int64      `json:"composite_group_id"`
	TargetGroupID    int64      `json:"target_group_id"`
	Priority         int        `json:"priority"`
	Enabled          bool       `json:"enabled"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DeletedAt        *time.Time `json:"deleted_at,omitempty"`
	TargetGroup      *Group     `json:"-"`
	TargetGroupInfo  *CompositeTargetGroupInfo `json:"target_group,omitempty"`
}

type CompositeTargetGroupInfo struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Status   string `json:"status"`
}

type CompositeGroupTargetInput struct {
	TargetGroupID int64
	Priority      int
	Enabled       bool
}

type CompositeGroupTargetRepository interface {
	ListByCompositeGroup(ctx context.Context, compositeGroupID int64, includeDisabled bool) ([]CompositeGroupTarget, error)
	Create(ctx context.Context, target *CompositeGroupTarget) error
	Update(ctx context.Context, target *CompositeGroupTarget) error
	Delete(ctx context.Context, compositeGroupID, targetID int64) error
}
