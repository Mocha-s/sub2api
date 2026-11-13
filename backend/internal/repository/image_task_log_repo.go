package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type imageTaskLogRepository struct{ db *sql.DB }

func NewImageTaskLogRepository(db *sql.DB) service.ImageTaskLogRepository {
	return &imageTaskLogRepository{db: db}
}

func (r *imageTaskLogRepository) Save(ctx context.Context, e *service.ImageTaskLogEntry) error {
	results, err := json.Marshal(e.Results)
	if err != nil {
		return err
	}
	if e.Results == nil {
		results = []byte("[]")
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO image_task_logs
 (task_id,user_id,api_key_id,request_id,request_mode,platform,model,prompt,status,item_count,success_count,error_code,error_message,results,created_at,finished_at,deadline_at)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14::jsonb,$15,$16,$17
 WHERE $17::timestamptz>NOW()-INTERVAL '72 hours' AND COALESCE($16::timestamptz,$17::timestamptz)>NOW()-INTERVAL '72 hours'
 ON CONFLICT (task_id) DO UPDATE SET
 request_id=EXCLUDED.request_id,platform=EXCLUDED.platform,model=EXCLUDED.model,prompt=EXCLUDED.prompt,
 status=EXCLUDED.status,item_count=EXCLUDED.item_count,success_count=EXCLUDED.success_count,
 error_code=EXCLUDED.error_code,error_message=EXCLUDED.error_message,results=EXCLUDED.results,finished_at=EXCLUDED.finished_at
 WHERE image_task_logs.status='processing' AND image_task_logs.deadline_at>NOW()-INTERVAL '72 hours'`,
		e.TaskID, e.UserID, e.APIKeyID, e.RequestID, e.RequestMode, e.Platform, e.Model, e.Prompt, e.Status, e.ItemCount, e.SuccessCount,
		e.ErrorCode, e.ErrorMessage, results, e.CreatedAt, e.FinishedAt, e.DeadlineAt)
	return err
}

func (r *imageTaskLogRepository) Get(ctx context.Context, id string, userID int64, index int) (*service.ImageTaskLogEntry, error) {
	if index < 0 {
		return nil, service.ErrImageTaskNotFound
	}
	var e service.ImageTaskLogEntry
	var results []byte
	err := r.db.QueryRowContext(ctx, `SELECT task_id,user_id,
 CASE WHEN status='processing' AND deadline_at<=NOW() THEN 'expired' ELSE status END,
  COALESCE(results->$3::int,'null'::jsonb) FROM image_task_logs WHERE task_id=$1 AND ($2::bigint=0 OR user_id=$2)
  AND COALESCE(finished_at,deadline_at) > NOW()-INTERVAL '72 hours'`, id, userID, index).Scan(&e.TaskID, &e.UserID, &e.Status, &results)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrImageTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	var image *service.ImageTaskLogResult
	if err = json.Unmarshal(results, &image); err != nil {
		return nil, err
	}
	if image != nil {
		e.Results = []service.ImageTaskLogResult{*image}
	}
	return &e, nil
}
