ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS description VARCHAR(500) NOT NULL DEFAULT '';
