-- 0014: questions 扩展 —— 方向归属 / 难度分层 / 去重键 / 清洗状态
-- 契约来源：docs/plans/eval-and-cleaning-plan.md 第 2 节

ALTER TABLE questions
  ADD COLUMN IF NOT EXISTS direction_domain_id BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS difficulty TEXT NOT NULL DEFAULT 'medium',
  ADD COLUMN IF NOT EXISTS difficulty_score INTEGER NOT NULL DEFAULT 2,
  ADD COLUMN IF NOT EXISTS dedupe_key TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'ai',
  ADD COLUMN IF NOT EXISTS cleaning_status TEXT NOT NULL DEFAULT 'clean';

CREATE INDEX IF NOT EXISTS idx_questions_dataset_dedupe ON questions (dataset_id, dedupe_key);
CREATE INDEX IF NOT EXISTS idx_questions_direction ON questions (dataset_id, direction_domain_id);
CREATE INDEX IF NOT EXISTS idx_questions_cleaning ON questions (dataset_id, cleaning_status);
