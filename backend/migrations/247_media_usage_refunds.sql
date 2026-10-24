-- 247_media_usage_refunds.sql
-- Durable, idempotent compensation ledger for successful media requests that
-- did not deliver all requested outputs.

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS refund_status VARCHAR(24) NOT NULL DEFAULT 'none';

DO $$ BEGIN
    ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_refund_status_check
        CHECK (refund_status IN ('none','pending','refunded'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE TABLE IF NOT EXISTS media_usage_refund_snapshots (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    group_id BIGINT,
    subscription_id BIGINT,
    platform VARCHAR(50) NOT NULL DEFAULT '',
    billing_type SMALLINT NOT NULL,
    total_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    actual_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    balance_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    subscription_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    api_key_quota_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    api_key_rate_limit_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    account_quota_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    platform_quota_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    account_stats_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    window_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (request_id, api_key_id)
);

CREATE TABLE IF NOT EXISTS media_usage_refund_events (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL,
    api_key_id BIGINT NOT NULL,
    scope VARCHAR(32) NOT NULL,
    requested_units INT NOT NULL,
    delivered_units INT NOT NULL,
    refund_ratio NUMERIC(20,10) NOT NULL,
    refunded_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    refunded_total_cost NUMERIC(20,10) NOT NULL DEFAULT 0,
    state VARCHAR(24) NOT NULL DEFAULT 'pending',
    reason TEXT NOT NULL,
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    snapshot_id BIGINT NOT NULL REFERENCES media_usage_refund_snapshots(id) ON DELETE RESTRICT,
    CONSTRAINT media_usage_refund_events_unique UNIQUE (request_id, api_key_id, scope),
    CONSTRAINT media_usage_refund_events_units_check CHECK (
        requested_units > 0 AND delivered_units >= 0 AND delivered_units <= requested_units
    ),
    CONSTRAINT media_usage_refund_events_ratio_check CHECK (refund_ratio >= 0 AND refund_ratio <= 1),
    CONSTRAINT media_usage_refund_events_amounts_check CHECK (refunded_cost >= 0 AND refunded_total_cost >= 0),
    CONSTRAINT media_usage_refund_events_state_check CHECK (state IN ('pending', 'refunded', 'failed')),
    CONSTRAINT media_usage_refund_events_completion_check CHECK (
        (state = 'refunded' AND completed_at IS NOT NULL)
        OR (state <> 'refunded' AND completed_at IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_media_usage_refund_events_pending
    ON media_usage_refund_events (state, updated_at, id)
    WHERE state <> 'refunded';
