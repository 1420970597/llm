-- 0012: 数据清洗 —— 拒答关键词库 / 规则 / 清洗运行 / 命中明细
-- 契约来源：docs/plans/eval-and-cleaning-plan.md 第 2 节

CREATE TABLE IF NOT EXISTS cleaning_keywords (
  id BIGSERIAL PRIMARY KEY,
  pattern TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT 'refusal',
  match_mode TEXT NOT NULL DEFAULT 'contains',
  severity TEXT NOT NULL DEFAULT 'block',
  is_builtin BOOLEAN NOT NULL DEFAULT FALSE,
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  note TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (pattern, category)
);
CREATE INDEX IF NOT EXISTS idx_cleaning_keywords_active ON cleaning_keywords (is_active, category);

CREATE TABLE IF NOT EXISTS cleaning_rules (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  stage_scope JSONB NOT NULL DEFAULT '["question","reasoning","answer"]'::jsonb,
  min_hits INTEGER NOT NULL DEFAULT 1,
  action TEXT NOT NULL DEFAULT 'flag',
  priority INTEGER NOT NULL DEFAULT 100,
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS cleaning_runs (
  id BIGSERIAL PRIMARY KEY,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  stages JSONB NOT NULL DEFAULT '["question","reasoning","answer"]'::jsonb,
  status TEXT NOT NULL DEFAULT 'queued',
  scanned_items INTEGER NOT NULL DEFAULT 0,
  flagged_items INTEGER NOT NULL DEFAULT 0,
  dropped_items INTEGER NOT NULL DEFAULT 0,
  report JSONB NOT NULL DEFAULT '{}'::jsonb,
  error_summary TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cleaning_runs_dataset ON cleaning_runs (dataset_id, status);

CREATE TABLE IF NOT EXISTS cleaning_findings (
  id BIGSERIAL PRIMARY KEY,
  cleaning_run_id BIGINT NOT NULL REFERENCES cleaning_runs(id) ON DELETE CASCADE,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  question_id BIGINT NOT NULL DEFAULT 0,
  stage TEXT NOT NULL,
  keyword_id BIGINT NOT NULL DEFAULT 0,
  matched_text TEXT NOT NULL DEFAULT '',
  snippet TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT 'flag',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_cleaning_findings_run ON cleaning_findings (cleaning_run_id, stage);
