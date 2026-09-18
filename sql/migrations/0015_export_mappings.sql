-- 0015: 多格式导出 —— 字段映射可配置
-- 契约来源：docs/plans/eval-and-cleaning-plan.md 第 2 节

CREATE TABLE IF NOT EXISTS export_mappings (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  format TEXT NOT NULL DEFAULT 'jsonl',
  target_kind TEXT NOT NULL DEFAULT 'sft',
  field_map JSONB NOT NULL DEFAULT '{}'::jsonb,
  options JSONB NOT NULL DEFAULT '{}'::jsonb,
  is_builtin BOOLEAN NOT NULL DEFAULT FALSE,
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
