-- Keep financial task rows and a permanent redaction marker; never DELETE video_tasks (settlements cascade).
ALTER TABLE video_tasks ADD COLUMN IF NOT EXISTS retention_scrubbed_at TIMESTAMPTZ;
ALTER TABLE batch_image_jobs ADD COLUMN IF NOT EXISTS retention_scrubbed_at TIMESTAMPTZ;

-- Freeze legacy terminal timestamps before cleanup/reconciliation can change updated_at.
UPDATE video_tasks SET completed_at=updated_at
WHERE completed_at IS NULL AND status IN ('completed','failed','cancelled','expired');
-- A delayed poller restart must not give an already timed-out task a fresh retention window.
UPDATE video_tasks SET completed_at=LEAST(completed_at,COALESCE(submitted_at,created_at)+INTERVAL '24 hours')
WHERE status='expired' AND provider_status='local_timeout';
UPDATE batch_image_jobs SET finished_at=updated_at
WHERE finished_at IS NULL AND status IN ('completed','failed','cancelled','output_deleted');

CREATE INDEX IF NOT EXISTS idx_image_task_logs_retention ON image_task_logs ((COALESCE(finished_at,deadline_at)));
CREATE INDEX IF NOT EXISTS idx_video_tasks_retention ON video_tasks ((COALESCE(completed_at,updated_at)))
WHERE retention_scrubbed_at IS NULL AND status IN ('completed','failed','cancelled','expired');
