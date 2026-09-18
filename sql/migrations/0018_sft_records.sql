-- SFT 训练样本（一等公民表）
--
-- 为什么不复用 reasoning_records：
--   reasoning_records 是「推理生成」阶段的旧语义（answer_summary + MinIO 对象键），
--   而 SFT 样本需要结构化保存 问题 → 思维链 → 答案 以及所对齐的长链标准步骤。
--   两者生命周期与字段语义都不同，混用会让 SFT 样本被后续推理生成覆盖。

CREATE TABLE IF NOT EXISTS sft_records (
  id BIGSERIAL PRIMARY KEY,
  dataset_id BIGINT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
  question_id BIGINT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
  domain_id BIGINT NOT NULL DEFAULT 0,
  chain_of_thought TEXT NOT NULL DEFAULT '',
  answer TEXT NOT NULL DEFAULT '',
  chain_steps JSONB NOT NULL DEFAULT '[]'::jsonb,
  status TEXT NOT NULL DEFAULT 'generated',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (dataset_id, question_id)
);

CREATE INDEX IF NOT EXISTS idx_sft_records_dataset ON sft_records (dataset_id);
