package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CleanupExpired only removes optional image logs. Video/batch rows and every financial
// table stay intact. SKIP LOCKED avoids waiting for work/settlement transactions.
func (r *imageTaskLogRepository) CleanupExpired(ctx context.Context, now time.Time, limit int) error {
	limit = min(max(limit, 1), 100)
	cutoff := now.Add(-service.MediaRetention)
	queries := []string{
		`WITH due AS (
 SELECT task_id FROM image_task_logs WHERE COALESCE(finished_at,deadline_at)<=$1
 ORDER BY COALESCE(finished_at,deadline_at),task_id LIMIT $2 FOR UPDATE SKIP LOCKED
 ) DELETE FROM image_task_logs i USING due WHERE i.task_id=due.task_id`,
		`WITH due AS (
 SELECT t.id FROM video_tasks t LEFT JOIN video_task_settlements s ON s.video_task_id=t.id
 WHERE t.retention_scrubbed_at IS NULL AND t.status IN ('completed','failed','cancelled','expired')
 AND COALESCE(t.completed_at,t.updated_at)<=$1
 AND (t.locked_until IS NULL OR t.locked_until<=NOW())
 AND ((s.id IS NULL AND NOT COALESCE(t.request_metadata->'request_metadata','{}'::jsonb) ? 'video_pricing_snapshot'
       AND NOT t.request_metadata ? 'video_pricing_snapshot'
       AND NOT t.request_metadata ? 'reconciliation_accepted_snapshot'
       AND NOT t.request_metadata ? 'reconciliation_upstream_task_id')
 OR (s.state IN ('released','refunded','charged') AND (s.state<>'charged' OR t.status='completed')
     AND s.next_reconcile_at IS NULL AND (s.locked_until IS NULL OR s.locked_until<=NOW())))
 ORDER BY COALESCE(t.completed_at,t.updated_at),t.id LIMIT $2 FOR UPDATE OF t SKIP LOCKED
 ) UPDATE video_tasks t SET prompt='',request_body=NULL,upstream_response=NULL,upstream_response_body=NULL,
 result_url=NULL,result_content_type=NULL,error_message=NULL,
 completed_at=COALESCE(t.completed_at,t.updated_at),retention_scrubbed_at=NOW(),
 result_metadata='{"retention_expired":true}'::jsonb,
 request_metadata=jsonb_strip_nulls(jsonb_build_object(
 'video_task_endpoint',t.request_metadata->'video_task_endpoint',
 'video_adapter',t.request_metadata->'video_adapter',
 'requested_model',t.request_metadata->'requested_model','upstream_model',t.request_metadata->'upstream_model',
 'billing_model',t.request_metadata->'billing_model',
 'request_metadata',jsonb_build_object('video_pricing_snapshot',t.request_metadata->'request_metadata'->'video_pricing_snapshot')))
 FROM due WHERE t.id=due.id`,
		`WITH due AS (
 SELECT id FROM batch_image_jobs WHERE retention_scrubbed_at IS NULL
 AND status IN ('completed','failed','cancelled','output_deleted') AND settled_at IS NOT NULL
 AND COALESCE(finished_at,updated_at)<=$1 ORDER BY COALESCE(finished_at,updated_at),id LIMIT $2 FOR UPDATE SKIP LOCKED
 ) UPDATE batch_image_jobs j SET task_name='',last_error_message=NULL,
 finished_at=COALESCE(j.finished_at,j.updated_at),retention_scrubbed_at=NOW() FROM due WHERE j.id=due.id`,
		`WITH due AS (
 SELECT i.id FROM batch_image_items i JOIN batch_image_jobs j ON j.batch_id=i.job_id
 WHERE j.retention_scrubbed_at IS NOT NULL AND COALESCE(j.finished_at,j.updated_at)<=$1
 AND (i.prompt_preview IS NOT NULL OR i.error_message IS NOT NULL)
 ORDER BY i.id LIMIT $2 FOR UPDATE OF i SKIP LOCKED
 ) UPDATE batch_image_items i SET prompt_preview=NULL,error_message=NULL FROM due WHERE i.id=due.id`,
	}
	for _, query := range queries {
		if _, err := r.db.ExecContext(ctx, query, cutoff, limit); err != nil {
			return err
		}
	}
	return nil
}
