package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type taskLogRepository struct{ db *sql.DB }

func NewTaskLogRepository(db *sql.DB) service.TaskLogRepository {
	return &taskLogRepository{db: db}
}

// shortcut: 两张表 UNION 后再排序 + OFFSET 分页，深分页会变慢；数据量明显增长后改游标分页。
const taskLogUnionSQL = `(
	SELECT 'video' AS type, t.public_task_id AS task_id, t.platform AS platform, t.requested_model AS model,
		t.status AS raw_status, t.progress AS progress, t.created_at AS submitted_at, t.completed_at AS finished_at,
		COALESCE(s.pricing_snapshot->'effective'->>'resolution','') AS resolution,
		COALESCE((s.pricing_snapshot->'effective'->>'seconds')::int,0) AS seconds,
		0 AS item_count, 0 AS success_count, 0 AS fail_count,
		COALESCE(s.actual_cost_usd - s.refunded_cost_usd, t.billed_usd)::float8 AS cost_usd,
		t.prompt AS prompt, COALESCE(t.error_code,'') AS error_code, COALESCE(t.error_message,'') AS error_message,
		(t.status='completed') AS has_result, COALESCE(t.result_url,'') AS result_url,
		COALESCE(s.state,'') AS settlement_state,
		COALESCE(s.actual_cost_usd,0)::float8 AS actual_cost_usd, COALESCE(s.refunded_cost_usd,0)::float8 AS refunded_cost_usd,
		t.user_id AS user_id, t.user_deleted_at AS user_deleted_at
	FROM video_tasks t LEFT JOIN video_task_settlements s ON s.video_task_id=t.id
	UNION ALL
	SELECT 'batch_image', j.batch_id, j.provider, j.model,
		j.status,
		CASE WHEN j.item_count>0 THEN LEAST(100,(j.success_count+j.fail_count+j.cancelled_count)*100/j.item_count) ELSE 0 END,
		j.created_at, j.finished_at,
		'', 0,
		j.item_count, j.success_count, j.fail_count,
		COALESCE(j.actual_cost, j.estimated_cost)::float8,
		j.task_name, COALESCE(j.last_error_code,''), COALESCE(j.last_error_message,''),
		(j.status='completed' AND j.api_key_id IS NOT NULL), '',
		'',
		0::float8, 0::float8,
		j.user_id, j.user_deleted_at
	FROM batch_image_jobs j
) x`

func taskLogWhere(f service.TaskLogFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, arg any) {
		args = append(args, arg)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if f.UserID > 0 {
		add("x.user_id = $%d", f.UserID)
	}
	if f.ExcludeUserDeleted {
		conds = append(conds, "x.user_deleted_at IS NULL")
	}
	if f.Type != "" {
		add("x.type = $%d", f.Type)
	}
	if f.Platform != "" {
		add("x.platform = $%d", f.Platform)
	}
	if f.TaskID != "" {
		add("x.task_id = $%d", f.TaskID)
	}
	if f.Start != nil {
		add("x.submitted_at >= $%d", *f.Start)
	}
	if f.End != nil {
		add("x.submitted_at < $%d", *f.End)
	}
	if f.Status != "" {
		args = append(args,
			pq.Array(service.TaskLogRawStatuses(service.TaskLogTypeVideo, f.Status)),
			pq.Array(service.TaskLogRawStatuses(service.TaskLogTypeBatchImage, f.Status)))
		conds = append(conds, fmt.Sprintf(
			"((x.type='video' AND x.raw_status = ANY($%d)) OR (x.type='batch_image' AND x.raw_status = ANY($%d)))",
			len(args)-1, len(args)))
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (r *taskLogRepository) List(ctx context.Context, params pagination.PaginationParams, filter service.TaskLogFilter) ([]service.TaskLog, *pagination.PaginationResult, error) {
	where, args := taskLogWhere(filter)

	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+taskLogUnionSQL+where, args...).Scan(&total); err != nil {
		return nil, nil, err
	}

	query := `SELECT x.type,x.task_id,x.platform,x.model,x.raw_status,x.progress,x.submitted_at,x.finished_at,
		x.resolution,x.seconds,x.item_count,x.success_count,x.fail_count,x.cost_usd,x.prompt,x.error_code,x.error_message,
		x.has_result,x.result_url,x.settlement_state,x.actual_cost_usd,x.refunded_cost_usd,x.user_id,COALESCE(u.email,'')
		FROM ` + taskLogUnionSQL + ` LEFT JOIN users u ON u.id=x.user_id` + where +
		fmt.Sprintf(" ORDER BY x.submitted_at DESC, x.task_id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
	rows, err := r.db.QueryContext(ctx, query, append(args, params.Limit(), params.Offset())...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.TaskLog, 0, params.Limit())
	for rows.Next() {
		var item service.TaskLog
		var finishedAt sql.NullTime
		if err := rows.Scan(&item.Type, &item.TaskID, &item.Platform, &item.Model, &item.RawStatus, &item.Progress, &item.SubmittedAt, &finishedAt,
			&item.Resolution, &item.Seconds, &item.ItemCount, &item.SuccessCount, &item.FailCount, &item.CostUSD, &item.Prompt, &item.ErrorCode, &item.ErrorMessage,
			&item.HasResult, &item.ResultURL, &item.SettlementState, &item.ActualCostUSD, &item.RefundedCostUSD, &item.UserID, &item.UserEmail); err != nil {
			return nil, nil, err
		}
		if finishedAt.Valid {
			item.FinishedAt = &finishedAt.Time
		}
		item.Status = service.TaskLogStatus(item.Type, item.RawStatus)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return items, paginationResultFromTotal(total, params), nil
}
