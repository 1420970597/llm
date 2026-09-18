-- 0019: 清洗运行记录用户显式指定的规则集合
-- 契约来源：docs/plans/eval-and-cleaning-plan.md 第 3.12 节
--   POST /api/v1/datasets/{id}/cleaning/run 的请求体为
--   { "stages": [...], "ruleIds": [] }。
--
-- 为什么需要这一列：
--   0012 建 cleaning_runs 时只落了 stages，没有落 ruleIds。结果是请求体里的
--   ruleIds 被 API 静默忽略——用户勾了「本次只用这几条规则」，清洗却仍按全部
--   启用规则执行，且没有任何报错。这类「选了却不生效」的静默失效比直接报错更糟，
--   因为它让用户以为自己控制住了清洗范围。
--
-- 语义（本次冻结，向后兼容）：
--   rule_ids = '{}'（空数组，也是既有行的默认值）
--       → 沿用 0012 起的既有行为：使用全部 is_active = TRUE 的规则。
--   rule_ids 非空
--       → 本次清洗只使用这些规则，与规则自身的 is_active 无关
--         （用户显式按 ID 指定，显式优先于开关状态）。
--   请求里出现不存在的 ruleId 时 API 返回 400 并列出未知 ID，不静默丢弃。

ALTER TABLE cleaning_runs
  ADD COLUMN IF NOT EXISTS rule_ids BIGINT[] NOT NULL DEFAULT '{}';
