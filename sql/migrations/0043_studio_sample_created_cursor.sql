-- #160 T29: ListSamples orders and seeks by creation time, not update time.
-- The updated_at index cannot satisfy the keyset order. At 100K samples the
-- previous plan scans/sorts the project before returning a 100-row page.
CREATE INDEX IF NOT EXISTS idx_samples_project_created
  ON samples (project_id, created_at DESC, id DESC);
