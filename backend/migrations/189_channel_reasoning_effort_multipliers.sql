ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS max_reasoning_effort_multiplier NUMERIC,
    ADD COLUMN IF NOT EXISTS reasoning_effort_multipliers JSONB NOT NULL DEFAULT '{}'::jsonb;
