-- Retain bounded generated image bytes, never request bodies or credentials.
CREATE TABLE IF NOT EXISTS image_task_logs (
    task_id TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    request_id TEXT NOT NULL DEFAULT '',
    request_mode TEXT NOT NULL CHECK (request_mode IN ('sync', 'async')),
    platform TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'processing',
    item_count INTEGER NOT NULL DEFAULT 1,
    success_count INTEGER NOT NULL DEFAULT 0,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    results JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    deadline_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT image_task_logs_counts CHECK (item_count >= 0 AND success_count >= 0 AND success_count <= item_count),
    CONSTRAINT image_task_logs_result_size CHECK (octet_length(results::text) <= 68157440)
);
CREATE INDEX IF NOT EXISTS idx_image_task_logs_user_created ON image_task_logs(user_id, created_at DESC, task_id DESC);
CREATE INDEX IF NOT EXISTS idx_image_task_logs_created ON image_task_logs(created_at DESC, task_id DESC);
