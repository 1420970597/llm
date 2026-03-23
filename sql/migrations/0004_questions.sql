CREATE TABLE IF NOT EXISTS questions (
  id BIGSERIAL PRIMARY KEY,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  domain_id BIGINT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  canonical_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'generated',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (dataset_id, canonical_hash)
);
