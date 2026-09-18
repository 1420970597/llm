-- 0016: 生成运行（断点续跑）+ datasets 扩展（n/m/x 用户可控、GRPO 档次、清洗开关）
-- 契约来源：docs/plans/eval-and-cleaning-plan.md 第 2 节

CREATE TABLE IF NOT EXISTS generation_runs (
  id BIGSERIAL PRIMARY KEY,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  stage TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  cursor JSONB NOT NULL DEFAULT '{}'::jsonb,
  total_units INTEGER NOT NULL DEFAULT 0,
  done_units INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  error_summary TEXT NOT NULL DEFAULT '',
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_generation_runs_lookup ON generation_runs (dataset_id, stage, status);

ALTER TABLE datasets
  ADD COLUMN IF NOT EXISTS target_kind TEXT NOT NULL DEFAULT 'sft',
  ADD COLUMN IF NOT EXISTS direction_count INTEGER NOT NULL DEFAULT 3,
  ADD COLUMN IF NOT EXISTS reward_levels JSONB NOT NULL DEFAULT '["-1","0","1"]'::jsonb,
  ADD COLUMN IF NOT EXISTS cleaning_enabled BOOLEAN NOT NULL DEFAULT TRUE,
  ADD COLUMN IF NOT EXISTS questions_per_direction INTEGER NOT NULL DEFAULT 5;
