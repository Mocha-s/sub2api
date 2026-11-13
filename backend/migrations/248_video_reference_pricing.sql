ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS video_reference_price_per_second NUMERIC(20,10);

ALTER TABLE channel_pricing_intervals
    ADD COLUMN IF NOT EXISTS video_reference_price_per_second NUMERIC(20,10);

ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS video_reference_price_per_second NUMERIC(20,10);

ALTER TABLE channel_account_stats_pricing_intervals
    ADD COLUMN IF NOT EXISTS video_reference_price_per_second NUMERIC(20,10);
