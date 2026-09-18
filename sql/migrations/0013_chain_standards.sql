-- 0013: 长链思维标准步骤 —— 每个方向一份，可编辑、可版本化
-- 契约来源：docs/plans/eval-and-cleaning-plan.md 第 2 节

CREATE TABLE IF NOT EXISTS chain_standards (
  id BIGSERIAL PRIMARY KEY,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  domain_id BIGINT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  direction_key TEXT NOT NULL DEFAULT '',
  current_version INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'generated',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (dataset_id, domain_id)
);
CREATE INDEX IF NOT EXISTS idx_chain_standards_dataset ON chain_standards (dataset_id, status);

CREATE TABLE IF NOT EXISTS chain_standard_versions (
  id BIGSERIAL PRIMARY KEY,
  standard_id BIGINT NOT NULL REFERENCES chain_standards(id) ON DELETE CASCADE,
  version INTEGER NOT NULL,
  steps JSONB NOT NULL DEFAULT '[]'::jsonb,
  source TEXT NOT NULL DEFAULT 'ai',
  change_note TEXT NOT NULL DEFAULT '',
  created_by BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (standard_id, version)
);
