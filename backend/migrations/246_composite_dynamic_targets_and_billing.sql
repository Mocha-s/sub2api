ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS dynamic_rate_billing_enabled BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS composite_group_targets (
    id BIGSERIAL PRIMARY KEY,
    composite_group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    target_group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    priority INTEGER NOT NULL DEFAULT 100,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_composite_group_targets_active_unique
    ON composite_group_targets (composite_group_id, target_group_id)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_composite_group_targets_group_enabled_priority
    ON composite_group_targets (composite_group_id, enabled, priority, id)
    WHERE deleted_at IS NULL;

ALTER TABLE composite_model_routes
    ADD COLUMN IF NOT EXISTS target_group_id BIGINT NULL REFERENCES groups(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_composite_model_routes_target_group
    ON composite_model_routes (target_group_id)
    WHERE target_group_id IS NOT NULL AND deleted_at IS NULL;
