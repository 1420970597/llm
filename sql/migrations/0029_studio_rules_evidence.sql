-- 0029: Atelier 版本化规则与不可变命中证据（Issue #160 T15）
--
-- 契约来源：docs/plans/atelier-implementation.md §2.3、§4.1（Decision 与证据分离）、
-- §5（rules 节点引用质量策略版本）；docs/plans/atelier-api-contract.md §2.6（规则预览）。
--
-- 本迁移要解决的问题（#160 T15 的原文判断）：
--   现有 cleaning_findings 是「扫描运行的一部分」，而扫描会**写回**
--   questions.cleaning_status（`ApplyCleaningStatus`，见 internal/store/cleaning_store_runs.go）。
--   于是：
--     * 预览与执行无法分开 —— 点一次「预览」就会改数据；
--     * 规则被改/删后，历史命中记录里只剩下规则 ID，无法回答
--       「当时那条规则长什么样、命中了哪个字段的哪个位置」；
--     * 规则自动结论与人工处置混在一列，无法区分「机器拦下的」与「人判断的」。
--
-- 三条不可让步的性质，用表结构而不是约定来保证：
--
--  1. **预览不落库**：预览端点在服务端只读（见 internal/store/rule_store.go）。
--     因此本迁移**不**为预览建表 —— 「预览前后无变化」不能靠「记得回滚」，
--     而要靠「根本没有写路径」。
--
--  2. **命中证据不可变**：rule_evidence 里冻结规则**表达式快照**、字段、
--     偏移与片段。规则后续被改/删都不影响历史命中 —— 否则「为什么当时
--     拦下了这条内容」永远无法复算。
--
--  3. **自动结论与人工处置分离**：rule_evidence 只记规则命中；
--     人工 Decision 在 T16 的表里（review_decisions）。两者不共表，
--     因此「规则自动写人工处置」在结构上不可能发生。

-- ---------------------------------------------------------------------------
-- rule_evaluations：一次规则执行的记录
-- ---------------------------------------------------------------------------
--
-- 为什么需要「执行」这一层：一次执行要能被整体解释（用了哪个质量策略版本、
-- 扫了哪些样本版本、什么时候跑的），而逐条命中挂在它下面。
-- 没有它，命中会变成一堆无法归属到某次动作的孤立行。

CREATE TABLE IF NOT EXISTS rule_evaluations (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  -- 引用被执行的**质量策略版本**（T04 的 document_versions 行）。
  -- 规则内容从它读取，并且命中证据会再冻结一份快照（见下）。
  quality_policy_version_id BIGINT NOT NULL REFERENCES document_versions(id) ON DELETE RESTRICT,
  purpose TEXT NOT NULL DEFAULT 'experiment'
    CHECK (purpose IN ('experiment', 'preview')),
  -- status 只描述「执行本身」的成功与否，不描述内容质量。
  -- 这一点很重要：把「有命中」当成「执行失败」会让规则命中被当成系统错误。
  status TEXT NOT NULL DEFAULT 'completed'
    CHECK (status IN ('running', 'completed', 'partial_failed', 'failed')),
  scanned_count INTEGER NOT NULL DEFAULT 0 CHECK (scanned_count >= 0),
  hit_count INTEGER NOT NULL DEFAULT 0 CHECK (hit_count >= 0),
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_rule_evaluations_project
  ON rule_evaluations (project_id, created_at DESC, id DESC);

-- ---------------------------------------------------------------------------
-- rule_evidence：不可变的命中证据
-- ---------------------------------------------------------------------------
--
-- 每一列都是「当时的事实」，全部来自执行那一刻的快照：
-- 规则表达式、字段、严重度、建议动作、命中偏移与片段。
--
-- 为什么把表达式快照存进来（而不是只存 rule_id）：
-- 规则会被改（用户发现误报后调表达式）。只存 ID 会让历史命中在规则被改后
-- 显示成新规则的结果 —— 而「当时为什么拦下它」是申诉与复核的唯一依据。

CREATE TABLE IF NOT EXISTS rule_evidence (
  id BIGSERIAL PRIMARY KEY,
  evaluation_id BIGINT NOT NULL REFERENCES rule_evaluations(id) ON DELETE CASCADE,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

  -- 被扫描的对象：具体的样本版本（不是「样本」）——
  -- 同一题的两次生成是两个不同的待检对象，合并会让「哪一版命中」无法回答。
  sample_id BIGINT NOT NULL,
  sample_version_id BIGINT NOT NULL REFERENCES sample_versions(id) ON DELETE RESTRICT,
  content_hash TEXT NOT NULL DEFAULT '',

  -- 规则身份 + **内容快照**。
  rule_id TEXT NOT NULL,
  rule_name TEXT NOT NULL DEFAULT '',
  rule_expression TEXT NOT NULL,
  rule_match_type TEXT NOT NULL DEFAULT '',
  -- severity 与 suggested_action 冻结在证据里：界面据此给处置建议，
  -- 而「建议」不能被后续规则修改影响（否则同一条证据在不同时间给出不同建议）。
  severity TEXT NOT NULL DEFAULT '',
  suggested_action TEXT NOT NULL DEFAULT '',

  field_name TEXT NOT NULL DEFAULT '',
  -- 偏移以**字符**（rune）计，与界面高亮一致；字节偏移会让中文内容的
  -- 高亮位置错位（一个汉字 3 字节）。
  match_start INTEGER NOT NULL DEFAULT 0 CHECK (match_start >= 0),
  match_end INTEGER NOT NULL DEFAULT 0 CHECK (match_end >= 0),
  snippet TEXT NOT NULL DEFAULT '',

  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rule_evidence_version
  ON rule_evidence (sample_version_id, rule_id);

CREATE INDEX IF NOT EXISTS idx_rule_evidence_evaluation
  ON rule_evidence (evaluation_id, id);

-- 「这条样本版本一共被哪些规则命中过」是审阅页的常用查询。
CREATE INDEX IF NOT EXISTS idx_rule_evidence_project_version
  ON rule_evidence (project_id, sample_version_id, id);
