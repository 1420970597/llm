ALTER TABLE model_providers
  ADD COLUMN IF NOT EXISTS reasoning_effort TEXT NOT NULL DEFAULT '';
