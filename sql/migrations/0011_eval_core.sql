-- 0011: 数据集评估 —— 维度 / 评估运行 / 裁判 / 条目 / 逐条打分 / 汇总
-- 契约来源：docs/plans/eval-and-cleaning-plan.md 第 2 节

CREATE TABLE IF NOT EXISTS eval_dimensions (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT 'long_chain',
  description TEXT NOT NULL DEFAULT '',
  rubric TEXT NOT NULL DEFAULT '',
  scale_min INTEGER NOT NULL DEFAULT 0,
  scale_max INTEGER NOT NULL DEFAULT 10,
  is_builtin BOOLEAN NOT NULL DEFAULT FALSE,
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  weight DOUBLE PRECISION NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_eval_dimensions_category ON eval_dimensions (category, is_active);

CREATE TABLE IF NOT EXISTS eval_runs (
  id BIGSERIAL PRIMARY KEY,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  sampling_mode TEXT NOT NULL DEFAULT 'full',
  sample_ratio DOUBLE PRECISION NOT NULL DEFAULT 0,
  sample_size INTEGER NOT NULL DEFAULT 0,
  target_kind TEXT NOT NULL DEFAULT 'sft',
  dimension_keys JSONB NOT NULL DEFAULT '[]'::jsonb,
  judge_provider_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
  generator_provider_id BIGINT NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'draft',
  total_items INTEGER NOT NULL DEFAULT 0,
  scored_items INTEGER NOT NULL DEFAULT 0,
  error_summary TEXT NOT NULL DEFAULT '',
  created_by BIGINT DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_eval_runs_dataset ON eval_runs (dataset_id, status);

CREATE TABLE IF NOT EXISTS eval_run_judges (
  id BIGSERIAL PRIMARY KEY,
  eval_run_id BIGINT NOT NULL REFERENCES eval_runs(id) ON DELETE CASCADE,
  provider_id BIGINT NOT NULL,
  provider_name TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  excluded BOOLEAN NOT NULL DEFAULT FALSE,
  exclude_reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  scored_items INTEGER NOT NULL DEFAULT 0,
  error_summary TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (eval_run_id, provider_id)
);

CREATE TABLE IF NOT EXISTS eval_items (
  id BIGSERIAL PRIMARY KEY,
  eval_run_id BIGINT NOT NULL REFERENCES eval_runs(id) ON DELETE CASCADE,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  question_id BIGINT NOT NULL,
  item_index INTEGER NOT NULL DEFAULT 0,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (eval_run_id, question_id)
);
CREATE INDEX IF NOT EXISTS idx_eval_items_run ON eval_items (eval_run_id, item_index);

CREATE TABLE IF NOT EXISTS eval_item_scores (
  id BIGSERIAL PRIMARY KEY,
  eval_run_id BIGINT NOT NULL REFERENCES eval_runs(id) ON DELETE CASCADE,
  eval_item_id BIGINT NOT NULL REFERENCES eval_items(id) ON DELETE CASCADE,
  judge_provider_id BIGINT NOT NULL,
  dimension_key TEXT NOT NULL,
  score DOUBLE PRECISION NOT NULL DEFAULT 0,
  rationale TEXT NOT NULL DEFAULT '',
  raw_response TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'scored',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (eval_item_id, judge_provider_id, dimension_key)
);
CREATE INDEX IF NOT EXISTS idx_eval_item_scores_run ON eval_item_scores (eval_run_id, judge_provider_id, dimension_key);

CREATE TABLE IF NOT EXISTS eval_summaries (
  id BIGSERIAL PRIMARY KEY,
  eval_run_id BIGINT NOT NULL REFERENCES eval_runs(id) ON DELETE CASCADE,
  scope TEXT NOT NULL,
  ref_key TEXT NOT NULL DEFAULT '',
  score DOUBLE PRECISION NOT NULL DEFAULT 0,
  sample_count INTEGER NOT NULL DEFAULT 0,
  detail JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (eval_run_id, scope, ref_key)
);
