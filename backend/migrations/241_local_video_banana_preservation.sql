ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS allow_video_generation BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS video_tasks (
    id BIGSERIAL PRIMARY KEY,
    public_task_id VARCHAR(80) NOT NULL UNIQUE,
    upstream_task_id VARCHAR(160),
    provider VARCHAR(64) NOT NULL,
    platform VARCHAR(64) NOT NULL,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    subscription_id BIGINT,
    account_id BIGINT NOT NULL,
    channel_id BIGINT,
    requested_model VARCHAR(200) NOT NULL,
    upstream_model VARCHAR(200) NOT NULL,
    billing_model VARCHAR(200) NOT NULL,
    model_mapping_chain VARCHAR(500),
    status VARCHAR(32) NOT NULL DEFAULT 'submitting',
    provider_status VARCHAR(64),
    progress INT NOT NULL DEFAULT 0,
    prompt TEXT NOT NULL,
    request_hash VARCHAR(64) NOT NULL,
    prompt_hash VARCHAR(64),
    request_body BYTEA,
    request_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    upstream_base_url VARCHAR(500),
    upstream_response JSONB,
    upstream_response_body BYTEA,
    result_url VARCHAR(1000),
    result_content_type VARCHAR(100),
    result_metadata JSONB,
    error_code VARCHAR(128),
    error_message TEXT,
    idempotency_key VARCHAR(255),
    idempotency_key_hash VARCHAR(64),
    usage_metadata JSONB,
    usage_log_id BIGINT,
    input_tokens INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    billed_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
    submitted_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    next_poll_at TIMESTAMPTZ,
    last_polled_at TIMESTAMPTZ,
    locked_until TIMESTAMPTZ,
    locked_by VARCHAR(128),
    poll_attempts INT NOT NULL DEFAULT 0,
    user_deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_tasks_progress_check CHECK (progress BETWEEN 0 AND 100),
    CONSTRAINT video_tasks_public_task_id_not_empty CHECK (public_task_id <> ''),
    CONSTRAINT video_tasks_status_check CHECK (status IN ('submitting','queued','in_progress','completed','failed','cancelled','expired','unknown'))
);

ALTER TABLE video_tasks ADD COLUMN IF NOT EXISTS user_deleted_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS video_tasks_api_key_id_idempotency_key_key ON video_tasks (api_key_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS video_tasks_public_task_id_key ON video_tasks (public_task_id);
CREATE INDEX IF NOT EXISTS idx_video_tasks_user_created ON video_tasks (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_video_tasks_account_status ON video_tasks (account_id, status);
CREATE INDEX IF NOT EXISTS idx_video_tasks_status_next_poll ON video_tasks (status, next_poll_at);
CREATE INDEX IF NOT EXISTS idx_video_tasks_request_hash ON video_tasks (request_hash);
CREATE INDEX IF NOT EXISTS video_tasks_user_deleted_at_idx ON video_tasks (user_deleted_at);

ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS video_price_per_second NUMERIC(20,10),
    ADD COLUMN IF NOT EXISTS video_default_seconds INT,
    ADD COLUMN IF NOT EXISTS video_allowed_seconds JSONB;
ALTER TABLE channel_pricing_intervals
    ADD COLUMN IF NOT EXISTS video_price_per_second NUMERIC(20,10);
ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS video_price_per_second NUMERIC(20,10),
    ADD COLUMN IF NOT EXISTS video_default_seconds INT,
    ADD COLUMN IF NOT EXISTS video_allowed_seconds JSONB;
ALTER TABLE channel_account_stats_pricing_intervals
    ADD COLUMN IF NOT EXISTS video_price_per_second NUMERIC(20,10);

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS refunded_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refunded_total_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refunded_account_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refund_reason TEXT,
    ADD COLUMN IF NOT EXISTS refunded_at TIMESTAMPTZ;
ALTER TABLE user_platform_quotas ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0;

DO $$ BEGIN
    ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_refunds_nonnegative_check CHECK (refunded_cost >= 0 AND refunded_total_cost >= 0 AND refunded_account_cost >= 0);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_refunds_not_over_gross_check CHECK (
        refunded_cost <= actual_cost AND refunded_total_cost <= total_cost
        AND refunded_account_cost <= COALESCE(account_stats_cost, total_cost * COALESCE(account_rate_multiplier, 1))
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_refund_metadata_check CHECK (
        (refunded_at IS NOT NULL OR (refunded_cost = 0 AND refunded_total_cost = 0 AND refunded_account_cost = 0 AND refund_reason IS NULL))
        AND (refund_reason IS NULL OR refunded_at IS NOT NULL)
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE usage_dashboard_hourly
    ADD COLUMN IF NOT EXISTS refunded_total_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refunded_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refunded_account_cost NUMERIC(20,10) NOT NULL DEFAULT 0;
ALTER TABLE usage_dashboard_daily
    ADD COLUMN IF NOT EXISTS refunded_total_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refunded_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS refunded_account_cost NUMERIC(20,10) NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS video_task_settlements (
    id BIGSERIAL PRIMARY KEY,
    video_task_id BIGINT NOT NULL REFERENCES video_tasks(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE RESTRICT,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    platform VARCHAR(50) NOT NULL,
    channel_id BIGINT REFERENCES channels(id) ON DELETE SET NULL,
    subscription_id BIGINT REFERENCES user_subscriptions(id) ON DELETE SET NULL,
    usage_log_id BIGINT REFERENCES usage_logs(id) ON DELETE SET NULL,
    charge_request_id VARCHAR(160) NOT NULL UNIQUE,
    state VARCHAR(24) NOT NULL,
    billing_type SMALLINT NOT NULL,
    gross_cost_usd NUMERIC(20,10) NOT NULL DEFAULT 0,
    actual_cost_usd NUMERIC(20,10) NOT NULL DEFAULT 0,
    account_cost_usd NUMERIC(20,10) NOT NULL DEFAULT 0,
    refunded_cost_usd NUMERIC(20,10) NOT NULL DEFAULT 0,
    pricing_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    effect_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    applied_snapshot JSONB,
    last_error TEXT,
    next_reconcile_at TIMESTAMPTZ,
    reconcile_attempts INT NOT NULL DEFAULT 0,
    locked_by VARCHAR(128),
    locked_until TIMESTAMPTZ,
    reserved_at TIMESTAMPTZ,
    charged_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    refunded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_task_settlements_task_unique UNIQUE (video_task_id),
    CONSTRAINT video_task_settlements_state_check CHECK (state IN ('reserved','charged','released','refunded')),
    CONSTRAINT video_task_settlements_billing_type_check CHECK (billing_type IN (0,1)),
    CONSTRAINT video_task_settlements_amounts_nonnegative_check CHECK (gross_cost_usd >= 0 AND actual_cost_usd >= 0 AND account_cost_usd >= 0 AND refunded_cost_usd >= 0)
);
CREATE TABLE IF NOT EXISTS video_task_settlement_events (
    id BIGSERIAL PRIMARY KEY,
    settlement_id BIGINT NOT NULL REFERENCES video_task_settlements(id) ON DELETE CASCADE,
    event_id VARCHAR(200) NOT NULL UNIQUE,
    event_type VARCHAR(24) NOT NULL,
    amount_usd NUMERIC(20,10) NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_task_settlement_events_unique UNIQUE (settlement_id, event_type),
    CONSTRAINT video_task_settlement_events_type_check CHECK (event_type IN ('reserve','capture','release','refund','legacy_refund')),
    CONSTRAINT video_task_settlement_events_amount_nonnegative_check CHECK (amount_usd >= 0)
);
CREATE INDEX IF NOT EXISTS idx_video_task_settlements_reconcile ON video_task_settlements (state, next_reconcile_at) WHERE state IN ('reserved','charged');
CREATE TABLE IF NOT EXISTS video_task_refund_reporting_jobs (
    id BIGSERIAL PRIMARY KEY,
    settlement_id BIGINT NOT NULL UNIQUE REFERENCES video_task_settlements(id) ON DELETE RESTRICT,
    usage_log_id BIGINT NOT NULL UNIQUE REFERENCES usage_logs(id) ON DELETE RESTRICT,
    usage_created_at TIMESTAMPTZ NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    locked_by VARCHAR(128),
    locked_until TIMESTAMPTZ,
    last_error TEXT,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_task_refund_reporting_jobs_settlement_unique UNIQUE (settlement_id),
    CONSTRAINT video_task_refund_reporting_jobs_usage_unique UNIQUE (usage_log_id),
    CONSTRAINT video_task_refund_reporting_jobs_settlement_fkey FOREIGN KEY (settlement_id) REFERENCES video_task_settlements(id) ON DELETE RESTRICT,
    CONSTRAINT video_task_refund_reporting_jobs_usage_fkey FOREIGN KEY (usage_log_id) REFERENCES usage_logs(id) ON DELETE RESTRICT,
    CONSTRAINT video_task_refund_reporting_jobs_attempts_check CHECK (attempts >= 0)
);

ALTER TABLE video_task_refund_reporting_jobs
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_settlement_id_fkey,
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_usage_log_id_fkey,
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_settlement_fkey,
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_usage_fkey;
ALTER TABLE video_task_refund_reporting_jobs
    ADD CONSTRAINT video_task_refund_reporting_jobs_settlement_fkey FOREIGN KEY (settlement_id) REFERENCES video_task_settlements(id) ON DELETE RESTRICT,
    ADD CONSTRAINT video_task_refund_reporting_jobs_usage_fkey FOREIGN KEY (usage_log_id) REFERENCES usage_logs(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_video_task_refund_reporting_jobs_due ON video_task_refund_reporting_jobs (next_attempt_at, locked_until, id) WHERE completed_at IS NULL;
CREATE TABLE IF NOT EXISTS video_task_cache_invalidation_jobs (
    id BIGSERIAL PRIMARY KEY,
    settlement_id BIGINT NOT NULL REFERENCES video_task_settlements(id) ON DELETE RESTRICT,
    event_type VARCHAR(24) NOT NULL,
    payload JSONB NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    locked_by VARCHAR(128),
    locked_until TIMESTAMPTZ,
    last_error TEXT,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    failed_at TIMESTAMPTZ,
    dead_letter_reason TEXT,
    CONSTRAINT video_task_cache_invalidation_jobs_unique UNIQUE (settlement_id, event_type),
    CONSTRAINT video_task_cache_invalidation_jobs_attempts_check CHECK (attempts >= 0)
);
CREATE INDEX IF NOT EXISTS idx_video_task_cache_invalidation_jobs_due ON video_task_cache_invalidation_jobs (next_attempt_at, locked_until, id) WHERE completed_at IS NULL;

ALTER TABLE composite_model_routes DROP CONSTRAINT IF EXISTS composite_model_routes_endpoint_check;
ALTER TABLE composite_model_routes ADD CONSTRAINT composite_model_routes_endpoint_check
    CHECK (endpoint IN ('any','messages','count_tokens','responses','chat_completions','embeddings','images','video','gemini'));
