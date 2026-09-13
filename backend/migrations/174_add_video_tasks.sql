-- Migration: 174_add_video_tasks
-- Persistent store for OpenAI-compatible asynchronous video generation tasks.

ALTER TABLE groups ADD COLUMN IF NOT EXISTS allow_video_generation BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS video_tasks (
    id                     BIGSERIAL PRIMARY KEY,
    public_task_id         VARCHAR(80)  NOT NULL,
    upstream_task_id       VARCHAR(160),

    provider               VARCHAR(64)  NOT NULL,
    platform               VARCHAR(64)  NOT NULL,
    user_id                BIGINT       NOT NULL,
    api_key_id             BIGINT       NOT NULL,
    group_id               BIGINT       NOT NULL,
    subscription_id        BIGINT,
    account_id             BIGINT       NOT NULL,
    channel_id             BIGINT,

    requested_model        VARCHAR(200) NOT NULL,
    upstream_model         VARCHAR(200) NOT NULL,
    billing_model          VARCHAR(200) NOT NULL,
    model_mapping_chain    VARCHAR(500),

    status                 VARCHAR(32)  NOT NULL DEFAULT 'submitting',
    provider_status        VARCHAR(64),
    progress               INT          NOT NULL DEFAULT 0,

    prompt                 TEXT         NOT NULL,
    request_hash           VARCHAR(64)  NOT NULL,
    prompt_hash            VARCHAR(64),
    request_body           BYTEA,
    request_metadata       JSONB        NOT NULL DEFAULT '{}'::jsonb,

    upstream_base_url      VARCHAR(500),
    upstream_response      JSONB,
    upstream_response_body BYTEA,

    result_url             VARCHAR(1000),
    result_content_type    VARCHAR(100),
    result_metadata        JSONB,
    error_code             VARCHAR(128),
    error_message          TEXT,
    idempotency_key        VARCHAR(255),
    idempotency_key_hash   VARCHAR(64),
    usage_metadata         JSONB,
    usage_log_id           BIGINT,
    input_tokens           INT          NOT NULL DEFAULT 0,
    output_tokens          INT          NOT NULL DEFAULT 0,
    billed_usd             DECIMAL(20,10) NOT NULL DEFAULT 0,

    submitted_at           TIMESTAMPTZ,
    started_at             TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ,
    expires_at             TIMESTAMPTZ,
    next_poll_at           TIMESTAMPTZ,
    last_polled_at         TIMESTAMPTZ,
    locked_until           TIMESTAMPTZ,
    locked_by              VARCHAR(128),
    poll_attempts          INT          NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),

    CONSTRAINT video_tasks_public_task_id_not_empty CHECK (public_task_id <> ''),
    CONSTRAINT video_tasks_progress_check CHECK (progress BETWEEN 0 AND 100),
    CONSTRAINT video_tasks_status_check CHECK (
        status IN ('submitting', 'queued', 'in_progress', 'completed', 'failed', 'cancelled', 'expired', 'unknown')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS video_tasks_public_task_id_key
    ON video_tasks (public_task_id);

CREATE UNIQUE INDEX IF NOT EXISTS video_tasks_api_key_id_idempotency_key_key
    ON video_tasks (api_key_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_video_tasks_user_created
    ON video_tasks (user_id, created_at);

CREATE INDEX IF NOT EXISTS idx_video_tasks_account_status
    ON video_tasks (account_id, status);

CREATE INDEX IF NOT EXISTS idx_video_tasks_status_next_poll
    ON video_tasks (status, next_poll_at);

CREATE INDEX IF NOT EXISTS idx_video_tasks_request_hash
    ON video_tasks (request_hash);
