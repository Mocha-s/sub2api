ALTER TABLE video_tasks
    ADD COLUMN IF NOT EXISTS user_deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS video_tasks_user_deleted_at_idx ON video_tasks (user_deleted_at);

COMMENT ON COLUMN video_tasks.user_deleted_at IS '用户侧删除/隐藏视频任务记录的时间；账务和对账记录仍保留';
