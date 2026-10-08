-- Conservative operator prices remain estimates even with provider token receipts.
ALTER TABLE model_price_versions ADD COLUMN IF NOT EXISTS is_estimated BOOLEAN NOT NULL DEFAULT FALSE;
