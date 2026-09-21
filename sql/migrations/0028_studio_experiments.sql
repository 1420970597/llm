-- 0028: Atelier 冻结质量实验与证据（Issue #160 T14）
--
-- 契约来源：docs/plans/atelier-implementation.md §2.3（质量分母与接纳率）、
-- §2.6（实验状态与缺分语义）、§4.1（Experiment 对象）、§4.3（evidence_revision）；
-- docs/plans/atelier-api-contract.md §2.5、§3.1（分列统计）。
--
-- 本迁移要解决的问题（#160 T14 的原文判断）：
--   现有 eval_runs 是「按 dataset + 当前维度 + 当前 provider」运行的，
--   于是：
--     * 跑完之后改维度/换模型/样本产生了新版本，**已经跑过的实验**看上去也变了
--       （报告里显示的是「现在的配置」，而不是「当时用的配置」）；
--     * 缺分与真实 0 分无法区分（都是 0），于是「没打分」被当成「打得很差」；
--     * 分母可以是「当前筛选出来的样本」，于是过滤掉差样本就能把接纳率做漂亮。
--
-- 三条不可让步的性质，本迁移用**表结构**而不是约定来保证：
--
--  1. **创建即冻结**：`experiments` 存 rubric/judges/seed 的**快照**，
--     `experiment_items` 存**具体的 sample_version 列表**。此后改维度、换
--     provider、样本产生新版本都不会改变这个实验 —— 报告永远能复算。
--
--  2. **缺分不是 0**：`experiment_scores.raw_score` 是**可空**列，
--     NULL 表示「这次没拿到分」。写 0 会让缺分被算成最差分，
--     从而把「没评」显示成「评得很差」（§2.6 明确区分）。
--
--  3. **分母固定且不可通过筛选变小**：分母 = `experiment_items` 的行数。
--     隔离样本**不缩小**分母（§2.3），它只是在投影里被单独计数。

-- ---------------------------------------------------------------------------
-- experiments：冻结的实验定义
-- ---------------------------------------------------------------------------
--
-- 为什么把 rubric/judges 存成 JSONB 快照而不是只存 ID 引用：
-- 「这次实验用的是哪套量表与哪些裁判」是**历史事实**。若只存 ID，
-- 那么管理员改一次量表权重，所有历史报告都会跟着变 —— 而报告是用来
-- 支撑「这批数据可以发布」的决策的，它必须能复算。
--
-- 同时保留 judge connection id：凭证每次现取（§2.4），但「是谁评的」
-- 必须可追溯，否则独立性检查与分歧分析无从谈起。

CREATE TABLE IF NOT EXISTS experiments (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  -- 批次可空：项目级质量实验（例如对已发布内容的抽检）不属于某个批次。
  batch_id BIGINT REFERENCES batches(id) ON DELETE SET NULL,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('sft', 'grpo')),

  status TEXT NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'running', 'partial_failed', 'completed', 'failed')),

  -- purpose 区分「质量检查」与「同基准比较」（T18）。两者的分母含义不同：
  -- 比较实验的分母是**配对完成数**，而质量检查的分母是冻结的样本范围。
  purpose TEXT NOT NULL DEFAULT 'quality' CHECK (purpose IN ('quality', 'comparison')),

  -- 抽样 seed：固定可复现。0 表示不抽样（全量）。
  sampling_seed BIGINT NOT NULL DEFAULT 0,

  -- 裁判快照：每一名裁判的连接、来源指纹与角色。
  -- 独立性判定依据 `endpoint_fingerprint`：同真实来源的**别名连接**
  -- （同 endpoint 不同 provider 行）不得自评。
  judges JSONB NOT NULL DEFAULT '[]'::jsonb,
  -- 量表快照：维度、权重、归一化范围。聚合时只用它，不查当前维度表。
  rubric JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- 生成来源快照（由样本来源**推导**，不接受客户端传入）。
  -- 用于「至少一名独立裁判」与「多生成来源逐条判断独立性」。
  generator_sources JSONB NOT NULL DEFAULT '[]'::jsonb,

  -- 缺分策略：exclude（缺分不计入该维度分母）或 fail_experiment（缺分即实验失败）。
  missing_score_policy TEXT NOT NULL DEFAULT 'exclude'
    CHECK (missing_score_policy IN ('exclude', 'fail_experiment')),

  -- 完成覆盖：已判定（scored）的项数。与 items 行数分开存放是为了让
  -- 「分母固定、分子增长」这件事在表上直接可见。
  inspected_count INTEGER NOT NULL DEFAULT 0 CHECK (inspected_count >= 0),
  scored_count INTEGER NOT NULL DEFAULT 0 CHECK (scored_count >= 0),
  missing_count INTEGER NOT NULL DEFAULT 0 CHECK (missing_count >= 0),
  error_count INTEGER NOT NULL DEFAULT 0 CHECK (error_count >= 0),

  created_by BIGINT REFERENCES users(id),
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_experiments_project
  ON experiments (project_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_experiments_batch
  ON experiments (batch_id)
  WHERE batch_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- experiment_items：冻结的样本范围（= 分母）
-- ---------------------------------------------------------------------------
--
-- 粒度是**样本版本**（不是样本）：同一题的两次生成是两个不同的待评对象，
-- 把它们合并会让「哪个版本通过了」无法回答。

CREATE TABLE IF NOT EXISTS experiment_items (
  id BIGSERIAL PRIMARY KEY,
  experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  sample_id BIGINT NOT NULL,
  -- ON DELETE RESTRICT：已进入实验的样本版本不得被删除。
  -- 用 CASCADE 会让「删一个样本」静默缩小历史实验的分母 —— 那正是
  -- 「分母可以被做小」的形态（§2.3 明确禁止）。
  sample_version_id BIGINT NOT NULL REFERENCES sample_versions(id) ON DELETE RESTRICT,
  content_hash TEXT NOT NULL DEFAULT '',

  -- 生成者身份**由样本来源推导**并冻结在这里。
  -- 为什么不接受客户端传入：请求体里带 generatorConnectionId 就能让
  -- 「生成者 == 裁判」看起来成立，从而绕过独立性检查（T14 验收项明确禁止）。
  generator_source TEXT NOT NULL DEFAULT '',
  generator_fingerprint TEXT NOT NULL DEFAULT '',

  -- 逐条状态。缺分（missing）与错误（error）与不适用（not_applicable）
  -- 是**三种不同的**事实，都不能用 0 代替（§2.6）。
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'scored', 'missing', 'error', 'not_applicable')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',

  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- 同一实验内同一版本只出现一次：重复会让分母翻倍。
  UNIQUE (experiment_id, sample_version_id)
);

CREATE INDEX IF NOT EXISTS idx_experiment_items_experiment
  ON experiment_items (experiment_id, id);

-- 「续跑只处理未完成的项」的索引（T14 验收项「失败项续跑不覆盖成功证据」）。
CREATE INDEX IF NOT EXISTS idx_experiment_items_pending
  ON experiment_items (experiment_id, status)
  WHERE status IN ('pending', 'error');

-- ---------------------------------------------------------------------------
-- experiment_scores：逐裁判逐维度的评分（只追加）
-- ---------------------------------------------------------------------------
--
-- 只追加而不是原地更新：同一裁判对同一版本的**更正**必须是新的一行，
-- 否则「第一次给了什么分」会消失，而它是判断「评分是否稳定」的唯一依据
-- （与 T16 的人工判断同一原则：更正通过 supersedes，不抹掉历史）。

CREATE TABLE IF NOT EXISTS experiment_scores (
  id BIGSERIAL PRIMARY KEY,
  experiment_id BIGINT NOT NULL REFERENCES experiments(id) ON DELETE CASCADE,
  experiment_item_id BIGINT NOT NULL REFERENCES experiment_items(id) ON DELETE CASCADE,

  judge_connection_id BIGINT NOT NULL,
  -- judge_index 是「第几名裁判」：同一连接可以在不同实验里充当不同角色，
  -- 而报告要能区分「裁判 1 与裁判 2 的分歧」。
  judge_index INTEGER NOT NULL DEFAULT 0 CHECK (judge_index >= 0),
  -- is_independent 冻结「这次评分是否算独立」的判断结果。
  -- 冻结而不每次重算：判定依赖连接指纹，而连接可被改/删，
  -- 重算会让历史报告的独立性结论漂移。
  is_independent BOOLEAN NOT NULL DEFAULT TRUE,

  dimension TEXT NOT NULL,

  -- **可空**：NULL = 缺分，不是 0（§2.6）。
  raw_score DOUBLE PRECISION,
  normalized_score DOUBLE PRECISION,
  -- score_state 说明这一格是什么：
  --   scored=有分；missing=没拿到分；error=裁判出错；not_applicable=该维度不适用。
  score_state TEXT NOT NULL DEFAULT 'scored'
    CHECK (score_state IN ('scored', 'missing', 'error', 'not_applicable')),

  rationale TEXT NOT NULL DEFAULT '',
  error_class TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- 幂等：同一 (项, 裁判, 维度) 只应有一条**当前**记录。
  -- 更正会先插入新行再把旧行标记为 superseded（superseded_by 非空即被取代）。
  superseded_by BIGINT
);

-- 「取当前有效评分」的部分唯一索引：被取代的行不参与唯一性，
-- 因此更正（插入新行 + 标记旧行）不会撞唯一约束。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_experiment_score_current
  ON experiment_scores (experiment_item_id, judge_connection_id, dimension)
  WHERE superseded_by IS NULL;

CREATE INDEX IF NOT EXISTS idx_experiment_scores_item
  ON experiment_scores (experiment_item_id, dimension);

CREATE INDEX IF NOT EXISTS idx_experiment_scores_experiment
  ON experiment_scores (experiment_id, dimension)
  WHERE superseded_by IS NULL;
