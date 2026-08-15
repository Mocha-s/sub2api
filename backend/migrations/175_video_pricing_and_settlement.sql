-- Add per-second video pricing, durable video settlements, and refund accounting.

ALTER TABLE channel_model_pricing
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

ALTER TABLE user_platform_quotas
    ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0;

DO $$ BEGIN
    ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_refunds_nonnegative_check CHECK (
        refunded_cost >= 0 AND refunded_total_cost >= 0 AND refunded_account_cost >= 0
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_refunds_not_over_gross_check CHECK (
        refunded_cost <= actual_cost
        AND refunded_total_cost <= total_cost
        AND refunded_account_cost <= COALESCE(account_stats_cost, total_cost * COALESCE(account_rate_multiplier, 1))
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_refund_metadata_check CHECK (
        (refunded_at IS NOT NULL OR (refunded_cost = 0 AND refunded_total_cost = 0 AND refunded_account_cost = 0 AND refund_reason IS NULL))
        AND (refund_reason IS NULL OR refunded_at IS NOT NULL)
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Existing actual_cost and total_cost columns remain gross values.
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
    charge_request_id VARCHAR(160) NOT NULL,
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
    CONSTRAINT video_task_settlements_charge_key_unique UNIQUE (charge_request_id),
    CONSTRAINT video_task_settlements_state_check CHECK (state IN ('reserved','charged','released','refunded')),
    CONSTRAINT video_task_settlements_billing_type_check CHECK (billing_type IN (0, 1)),
    CONSTRAINT video_task_settlements_amounts_nonnegative_check CHECK (
        gross_cost_usd >= 0 AND actual_cost_usd >= 0 AND account_cost_usd >= 0 AND refunded_cost_usd >= 0
    )
);

CREATE TABLE IF NOT EXISTS video_task_settlement_events (
    id BIGSERIAL PRIMARY KEY,
    settlement_id BIGINT NOT NULL REFERENCES video_task_settlements(id) ON DELETE CASCADE,
    event_id VARCHAR(200) NOT NULL,
    event_type VARCHAR(24) NOT NULL,
    amount_usd NUMERIC(20,10) NOT NULL DEFAULT 0,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT video_task_settlement_events_unique UNIQUE (settlement_id, event_type),
    CONSTRAINT video_task_settlement_events_event_id_unique UNIQUE (event_id),
    CONSTRAINT video_task_settlement_events_type_check CHECK (event_type IN ('reserve','capture','release','refund','legacy_refund')),
    CONSTRAINT video_task_settlement_events_amount_nonnegative_check CHECK (amount_usd >= 0)
);

CREATE INDEX IF NOT EXISTS idx_video_task_settlements_reconcile
    ON video_task_settlements (state, next_reconcile_at)
    WHERE state IN ('reserved','charged');

CREATE TABLE IF NOT EXISTS video_task_refund_reporting_jobs (
    id BIGSERIAL PRIMARY KEY,
    settlement_id BIGINT NOT NULL,
    usage_log_id BIGINT NOT NULL,
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

-- Fresh rollout: keep the final migration-175 foreign-key definitions idempotent within this SQL file.
ALTER TABLE video_task_refund_reporting_jobs
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_settlement_id_fkey,
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_usage_log_id_fkey,
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_settlement_fkey,
    DROP CONSTRAINT IF EXISTS video_task_refund_reporting_jobs_usage_fkey;
ALTER TABLE video_task_refund_reporting_jobs
    ADD CONSTRAINT video_task_refund_reporting_jobs_settlement_fkey FOREIGN KEY (settlement_id) REFERENCES video_task_settlements(id) ON DELETE RESTRICT,
    ADD CONSTRAINT video_task_refund_reporting_jobs_usage_fkey FOREIGN KEY (usage_log_id) REFERENCES usage_logs(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_video_task_refund_reporting_jobs_due
    ON video_task_refund_reporting_jobs (next_attempt_at, locked_until, id)
    WHERE completed_at IS NULL;

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
    CONSTRAINT video_task_cache_invalidation_jobs_unique UNIQUE (settlement_id, event_type),
    CONSTRAINT video_task_cache_invalidation_jobs_attempts_check CHECK (attempts >= 0)
);

CREATE INDEX IF NOT EXISTS idx_video_task_cache_invalidation_jobs_due
    ON video_task_cache_invalidation_jobs (next_attempt_at, locked_until, id)
    WHERE completed_at IS NULL;

ALTER TABLE video_task_cache_invalidation_jobs
    ADD COLUMN IF NOT EXISTS failed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS dead_letter_reason TEXT;

COMMENT ON COLUMN video_task_settlements.actual_cost_usd IS
    'Applied customer funding amount normalized to 8 decimal places to match balance/subscription DECIMAL(20,8) counters.';
