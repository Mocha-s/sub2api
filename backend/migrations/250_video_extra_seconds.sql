ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS video_extra_seconds INTEGER NOT NULL DEFAULT 0
    CHECK (video_extra_seconds BETWEEN 0 AND 3600);

ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS video_extra_seconds INTEGER NOT NULL DEFAULT 0
    CHECK (video_extra_seconds BETWEEN 0 AND 3600);
