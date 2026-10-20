package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/compositegrouptarget"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type compositeGroupTargetRepository struct {
	client *dbent.Client
	sql    *sql.DB
}

func NewCompositeGroupTargetRepository(client *dbent.Client, sqlDB *sql.DB) service.CompositeGroupTargetRepository {
	return &compositeGroupTargetRepository{client: client, sql: sqlDB}
}

func (r *compositeGroupTargetRepository) ListByCompositeGroup(ctx context.Context, compositeGroupID int64, includeDisabled bool) ([]service.CompositeGroupTarget, error) {
	q := clientFromContext(ctx, r.client).CompositeGroupTarget.Query().
		Where(compositegrouptarget.CompositeGroupIDEQ(compositeGroupID)).
		WithTargetGroup().
		Order(dbent.Asc(compositegrouptarget.FieldPriority), dbent.Asc(compositegrouptarget.FieldID))
	if !includeDisabled {
		q = q.Where(compositegrouptarget.EnabledEQ(true))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.CompositeGroupTarget, 0, len(rows))
	for _, row := range rows {
		out = append(out, compositeGroupTargetEntityToService(row))
	}
	return out, nil
}

func (r *compositeGroupTargetRepository) Create(ctx context.Context, target *service.CompositeGroupTarget) error {
	if target == nil {
		return fmt.Errorf("composite target is nil")
	}
	created, err := clientFromContext(ctx, r.client).CompositeGroupTarget.Create().
		SetCompositeGroupID(target.CompositeGroupID).
		SetTargetGroupID(target.TargetGroupID).
		SetPriority(target.Priority).
		SetEnabled(target.Enabled).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, service.ErrCompositeTargetExists)
	}
	*target = compositeGroupTargetEntityToService(created)
	return r.enqueueGroupChanged(ctx, target.CompositeGroupID)
}

func (r *compositeGroupTargetRepository) Update(ctx context.Context, target *service.CompositeGroupTarget) error {
	if target == nil {
		return service.ErrCompositeTargetNotFound
	}
	updated, err := clientFromContext(ctx, r.client).CompositeGroupTarget.UpdateOneID(target.ID).
		SetPriority(target.Priority).
		SetEnabled(target.Enabled).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrCompositeTargetNotFound, nil)
	}
	*target = compositeGroupTargetEntityToService(updated)
	return r.enqueueGroupChanged(ctx, target.CompositeGroupID)
}

func (r *compositeGroupTargetRepository) Delete(ctx context.Context, compositeGroupID, targetID int64) error {
	_, err := clientFromContext(ctx, r.client).CompositeGroupTarget.UpdateOneID(targetID).
		SetDeletedAt(time.Now().UTC()).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrCompositeTargetNotFound, nil)
	}
	return r.enqueueGroupChanged(ctx, compositeGroupID)
}

func (r *compositeGroupTargetRepository) enqueueGroupChanged(ctx context.Context, groupID int64) error {
	if r.sql == nil {
		return nil
	}
	return enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventGroupChanged, nil, &groupID, nil)
}

func compositeGroupTargetEntityToService(row *dbent.CompositeGroupTarget) service.CompositeGroupTarget {
	target := service.CompositeGroupTarget{
		ID:               row.ID,
		CompositeGroupID: row.CompositeGroupID,
		TargetGroupID:    row.TargetGroupID,
		Priority:         row.Priority,
		Enabled:          row.Enabled,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
		DeletedAt:        row.DeletedAt,
	}
	if row.Edges.TargetGroup != nil {
		target.TargetGroup = groupEntityToService(row.Edges.TargetGroup)
	}
	return target
}
