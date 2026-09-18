-- GRPO 教师模型评判提示词（一等公民表）
--
-- 为什么不复用 reward_records：
--   reward_records.question_id 带 UNIQUE 约束，且 apps/api/exports.go 在
--   reward 行数上设了导出硬门禁。GRPO 提示词与 SFT 奖励分是两种语义的数据，
--   叠加在同一行会互相覆盖，导致提示词静默丢失或导出 reward_score 全为 0。

CREATE TABLE IF NOT EXISTS grpo_prompts (
  id BIGSERIAL PRIMARY KEY,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  question_id BIGINT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
  domain_id BIGINT NOT NULL DEFAULT 0,
  levels JSONB NOT NULL DEFAULT '[]'::jsonb,
  judge_prompt TEXT NOT NULL DEFAULT '',
  level_rubrics JSONB NOT NULL DEFAULT '[]'::jsonb,
  framework_ref TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'generated',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (dataset_id, question_id)
);

CREATE INDEX IF NOT EXISTS idx_grpo_prompts_dataset ON grpo_prompts (dataset_id);
