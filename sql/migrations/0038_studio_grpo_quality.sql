-- 0038: GRPO 质量实验的 typed 目标配置（Issue #160 T24）
--
-- 契约来源：docs/plans/atelier-implementation.md §2.2（GRPO payload 字段）、
-- §2.3（分母与覆盖）、§2.6（缺分语义）；#160 T24 的原文要求。
--
-- 为什么需要这一列，而不是把 GRPO 配置塞进现有的 rubric/judges：
--
--   `rubric` 是**量表快照**（维度、权重、归一化范围），`judges` 是裁判快照；
--   两者对 SFT 与 GRPO 是同一种东西。而 T24 要求额外冻结的是
--   **教师提示词版本、基准回答版本与边界参考集**：它们是 GRPO 判定
--   「边界稳定性」的输入，改变它们会让同一批样本得到不同结论。
--
--   把它们塞进 rubric 会让「量表」这个字段同时承担两种语义，
--   而报告聚合只读 rubric 的维度 —— 于是多出来的键要么被静默忽略，
--   要么在某次重构里被当成维度处理。单列一列是更便宜的确定性。
--
-- 为什么是 JSONB 而不是展开成多列：
--   边界参考集是**一次性冻结的输入**（列数不固定，且只在判定时整体读取），
--   拆成 `boundary_reference_items` 子表会让「冻结」变成「可被外部 UPDATE
--   的引用数据」。JSONB + 内容 hash 让「这份实验用的是哪一份参考集」
--   可以由 hash 复算，与 T14/T20/T21 的快照原则一致。
--
-- 既有行不受影响：DEFAULT '{}' 使 SFT 实验读出零值配置，
-- 而 SFT 分支从不读取这一列。

ALTER TABLE experiments
  ADD COLUMN IF NOT EXISTS target_config JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN experiments.target_config IS
  'T24：GRPO 专属冻结配置（教师提示词版本 / 基准回答版本 / 边界参考集与 hash）。SFT 实验为零值。';
