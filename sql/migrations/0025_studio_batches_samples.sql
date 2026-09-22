-- 0025: Atelier 批次、单元与不可变样本版本（Issue #160 T05）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1、§4.2、§2.6；
-- docs/plans/atelier-api-contract.md §2.3、§2.4、§3.1。
--
-- 为什么需要这四张表（#160 §1 的根因）：
--   现有 `sft_records` / `grpo_prompts` 以 `(dataset_id, question_id)` upsert，
--   于是**重跑会覆盖历史**，「同一主题跑两次不同方案」的结果无法并存，
--   而「这个样本是哪个方案产生的」也无从追溯。
--   新路径把**身份**（samples）与**内容**（sample_versions）分开：
--   身份稳定、内容只追加。这是 T05 之后所有质量/发布语义的地基。

-- ---------------------------------------------------------------------------
-- batches：一次试制或扩量
-- ---------------------------------------------------------------------------
--
-- 「输入快照」是这一张表的核心价值：批次记录它**当时**引用的版本 ID 与内容 hash，
-- 而不是读项目的「当前采用」指针。因此保存新蓝图不会改变已提交批次的输入
--（T05 验收项「保存新蓝图不改旧快照」）。
--
-- 快照 ID 用 **版本行 ID**（document_versions.id）而不是版本号：版本号在
-- (document, logicalId) 内才唯一，而快照需要跨 logicalId 的全局身份。
-- 同时存 hash：即使版本行被误删重建，hash 不一致也能被发现。
--
-- 状态与控制状态**分离**（§2.6 与 T05 要求）：
--   * status 是「这次运行整体处于什么阶段」；
--   * control_state 是「用户/系统的控制意图」（run/pause_requested/paused）。
-- 合并成一个字段就无法表达「已经请求暂停，但在途请求还在跑」这一真实状态，
-- 而它正是 #159 反复强调的「暂停不撤回在途请求」。

CREATE TABLE IF NOT EXISTS batches (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  purpose TEXT NOT NULL CHECK (purpose IN ('pilot', 'scale')),
  -- 状态机见 §4.2。partial_failed 与 failed 区分：前者可幂等恢复失败项，
  -- 后者是致命错误（继续重试没有意义）。
  status TEXT NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'running', 'pause_requested', 'paused',
                      'partial_failed', 'completed', 'failed')),
  control_state TEXT NOT NULL DEFAULT 'run'
    CHECK (control_state IN ('run', 'pause_requested', 'paused')),

  -- 输入快照：版本行 ID + 内容 hash + 逻辑版本号（后者仅供展示与对账）。
  blueprint_version_id      BIGINT,
  blueprint_content_hash    TEXT NOT NULL DEFAULT '',
  coverage_version_id       BIGINT,
  coverage_content_hash     TEXT NOT NULL DEFAULT '',
  standard_version_id       BIGINT,
  standard_content_hash     TEXT NOT NULL DEFAULT '',
  quality_policy_version_id BIGINT,
  quality_policy_content_hash TEXT NOT NULL DEFAULT '',
  mapping_version_id        BIGINT,
  mapping_content_hash      TEXT NOT NULL DEFAULT '',
  -- 生成节点配置的完整快照：模型连接是**非秘密标识**，不含明文密钥（§2.4）。
  -- 它必须内联在批次里，而不是只引用蓝图版本 —— 因为「这一批用了什么并发和
  -- 输出上限」属于运行事实，蓝图后续可以改。
  generation_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('sft', 'grpo')),
  schema_version TEXT NOT NULL,

  -- 计划单元数 vs 实际产出分别统计（§2.1：计划量不等于已产出）。
  planned_units INTEGER NOT NULL DEFAULT 0 CHECK (planned_units >= 0),
  completed_units INTEGER NOT NULL DEFAULT 0 CHECK (completed_units >= 0),
  failed_units INTEGER NOT NULL DEFAULT 0 CHECK (failed_units >= 0),
  -- in_flight 是「已提交给外部供应商、尚未确认」的数量。
  -- 它必须可查询：暂停时要告诉用户「还有几个在途，停止新请求不等于立刻停费」（§2.4）。
  in_flight_units INTEGER NOT NULL DEFAULT 0 CHECK (in_flight_units >= 0),

  -- 预算：整数最小货币单位（分），币种显式（§2.4）。
  budget_currency TEXT NOT NULL DEFAULT 'CNY',
  budget_limit_minor BIGINT NOT NULL DEFAULT 0 CHECK (budget_limit_minor >= 0),

  -- coverage_slice 记录「只跑某个切片」的选择（T13 的扩量范围）。
  coverage_slice JSONB NOT NULL DEFAULT '{}'::jsonb,

  -- 租约（T06 实现抢占；这里先建列，避免 T06 再来一次迁移改批次表）。
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ,
  fencing_token BIGINT NOT NULL DEFAULT 0,

  created_by BIGINT REFERENCES users(id),
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 列表默认按「同项目内最近」排序，末位 id 保证全序（§1.5 游标分页）。
CREATE INDEX IF NOT EXISTS idx_batches_project_created
  ON batches (project_id, created_at DESC, id DESC);
-- 「同项目同 purpose 的活跃批次」是列表与概览的常用查询（不猜「最大 ID」）。
CREATE INDEX IF NOT EXISTS idx_batches_project_purpose_status
  ON batches (project_id, purpose, status);
-- 快照引用必须可追溯（「哪些批次用了这个蓝图版本」）。
CREATE INDEX IF NOT EXISTS idx_batches_blueprint ON batches (blueprint_version_id)
  WHERE blueprint_version_id IS NOT NULL;

-- 复合外键：批次的快照引用必须与批次同项目。
-- 单列外键只能保证「版本存在」，无法阻止「A 项目的批次引用 B 项目的蓝图」。
-- 迁移 0024 已建 document_versions (id, project_id) 唯一索引，这里直接复用。
ALTER TABLE batches
  DROP CONSTRAINT IF EXISTS batches_blueprint_same_project;
ALTER TABLE batches
  ADD CONSTRAINT batches_blueprint_same_project
  FOREIGN KEY (blueprint_version_id, project_id)
  REFERENCES document_versions (id, project_id) ON DELETE RESTRICT;

ALTER TABLE batches
  DROP CONSTRAINT IF EXISTS batches_coverage_same_project;
ALTER TABLE batches
  ADD CONSTRAINT batches_coverage_same_project
  FOREIGN KEY (coverage_version_id, project_id)
  REFERENCES document_versions (id, project_id) ON DELETE RESTRICT;

ALTER TABLE batches
  DROP CONSTRAINT IF EXISTS batches_standard_same_project;
ALTER TABLE batches
  ADD CONSTRAINT batches_standard_same_project
  FOREIGN KEY (standard_version_id, project_id)
  REFERENCES document_versions (id, project_id) ON DELETE RESTRICT;

ALTER TABLE batches
  DROP CONSTRAINT IF EXISTS batches_quality_policy_same_project;
ALTER TABLE batches
  ADD CONSTRAINT batches_quality_policy_same_project
  FOREIGN KEY (quality_policy_version_id, project_id)
  REFERENCES document_versions (id, project_id) ON DELETE RESTRICT;

ALTER TABLE batches
  DROP CONSTRAINT IF EXISTS batches_mapping_same_project;
ALTER TABLE batches
  ADD CONSTRAINT batches_mapping_same_project
  FOREIGN KEY (mapping_version_id, project_id)
  REFERENCES document_versions (id, project_id) ON DELETE RESTRICT;

-- ---------------------------------------------------------------------------
-- batch_steps：按阶段与**单位**分别记进度
-- ---------------------------------------------------------------------------
--
-- 为什么必须按单位而不是一个百分比（§2.6 与 issue #141 的既有教训）：
--   阶段进度用「题数」冒充「方向数」会让用户看到 100% 而实际只跑完方向。
--   因此每个阶段自己声明它的单位标签与总量，界面按单位分别展示。
--   T05 只建表与读写；真正的单位聚合在 T12/T13。

CREATE TABLE IF NOT EXISTS batch_steps (
  id BIGSERIAL PRIMARY KEY,
  batch_id BIGINT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
  phase TEXT NOT NULL,
  -- unit_label 是**人类可读的单位名**（方向 / 问题 / 样本），
  -- 界面直接展示它，不再从 phase 反推单位。
  unit_label TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'running', 'paused', 'partial_failed', 'completed', 'failed', 'skipped')),
  total_units INTEGER NOT NULL DEFAULT 0 CHECK (total_units >= 0),
  done_units INTEGER NOT NULL DEFAULT 0 CHECK (done_units >= 0),
  failed_units INTEGER NOT NULL DEFAULT 0 CHECK (failed_units >= 0),
  error_summary TEXT NOT NULL DEFAULT '',
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (batch_id, phase)
);

CREATE INDEX IF NOT EXISTS idx_batch_steps_batch ON batch_steps (batch_id, id);

-- ---------------------------------------------------------------------------
-- samples：**身份**（稳定、不含内容）
-- ---------------------------------------------------------------------------
--
-- sample_key 是「计划单元」的稳定标识（例如 domain/direction/question 的稳定 ID）。
-- 同一个计划单元在不同批次产出**不同版本**，因此：
--   * 同题不同批次的输出不互相覆盖（T05 验收项）；
--   * 「原样本重生成」是新版本，而不是新身份（T05 验收项）。
--
-- 为什么 sample_key 用 TEXT 而不是把 domain/direction/question 拆成三列：
-- SFT 与 GRPO 的计划单元粒度相同（都是「一道基准题」），但将来可能引入
-- 其它粒度；用稳定字符串键让粒度演进不必改表。同时它天然携带可读信息，
-- 排障时能一眼看出是哪个单元。
CREATE TABLE IF NOT EXISTS samples (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  sample_key TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK (target_kind IN ('sft', 'grpo')),
  -- title/summary 是**只读展示元数据**（问题标题），不是内容正文。
  -- 正文只在 sample_versions 里，因此判断永远不会改到它。
  title TEXT NOT NULL DEFAULT '',
  -- 计划事实：这个单元第一次是由哪个批次产生的（用于「首个来源」追溯）。
  origin_batch_id BIGINT REFERENCES batches(id) ON DELETE SET NULL,
  -- 当前最新版本号（不指内容；读具体内容必须显式指定版本）。
  latest_version INTEGER NOT NULL DEFAULT 0 CHECK (latest_version >= 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (project_id, sample_key)
);

CREATE INDEX IF NOT EXISTS idx_samples_project_updated
  ON samples (project_id, updated_at DESC, id DESC);

-- ---------------------------------------------------------------------------
-- sample_versions：**不可变内容版本**（只追加）
-- ---------------------------------------------------------------------------
--
-- 三条不可变性质（T05 验收项与 §4.1）：
--   1. 内容只追加：没有 UPDATE 路径，判断/规则/发布都不得改写这里；
--   2. provenance 冻结：批次、单元、尝试、以及**当时的**模型与标准配置，
--      全部内联在行里，不随旧表更新；
--   3. content_hash 在写入时对规范化 JSON 计算一次并存储，
--      使「内容是否变过」可离线核对（§2.2 的冻结语义）。
--
-- SFT 与 GRPO 的 payload schema 不同（§2.2），由 application 层校验；
-- 这里只强制 payload 是 JSON 对象，以及 schema_version 与 target_kind 匹配。
-- 为什么不把 schema 约束写成 CHECK：schema 演进应当只是代码变更 + 数据里的
-- 新取值，加 CHECK 会让每个新 schema 版本都变成一次 DDL 迁移。

CREATE TABLE IF NOT EXISTS sample_versions (
  id BIGSERIAL PRIMARY KEY,
  sample_id BIGINT NOT NULL REFERENCES samples(id) ON DELETE CASCADE,
  -- project_id 冗余：批次/发布/规则证据都要按项目过滤，联 samples 会让每条
  -- 查询多一次 join 且容易漏掉（漏掉就是串项目）。
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  version INTEGER NOT NULL CHECK (version >= 1),
  target_kind TEXT NOT NULL CHECK (target_kind IN ('sft', 'grpo')),
  schema_version TEXT NOT NULL,
  payload JSONB NOT NULL,
  content_hash TEXT NOT NULL,

  -- provenance：产出这次内容的批次、单元与尝试。
  batch_id BIGINT REFERENCES batches(id) ON DELETE SET NULL,
  batch_item_id BIGINT,
  attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt >= 1),
  -- 生成时使用的配置快照（模型连接**非秘密标识**、模型版本、并发、输出上限）。
  -- 与 batches.generation_config 有意重复：批次行将来可被归档或改写，
  -- 而「这条内容是哪个模型产出的」必须永久可查（§2.4 要求保留实际 provider 标识）。
  generator_config JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- 生成时引用的标准/蓝图版本与 hash：质量证据要能回答「按哪版标准生成的」。
  standard_version_id BIGINT,
  standard_content_hash TEXT NOT NULL DEFAULT '',
  blueprint_version_id BIGINT,
  blueprint_content_hash TEXT NOT NULL DEFAULT '',

  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (sample_id, version)
);

CREATE INDEX IF NOT EXISTS idx_sample_versions_project_created
  ON sample_versions (project_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_sample_versions_batch
  ON sample_versions (batch_id, id);
CREATE INDEX IF NOT EXISTS idx_sample_versions_hash
  ON sample_versions (content_hash);

-- 生成来源的标准/蓝图必须与样本版本同项目（同样的复合外键理由）。
ALTER TABLE sample_versions
  DROP CONSTRAINT IF EXISTS sample_versions_standard_same_project;
ALTER TABLE sample_versions
  ADD CONSTRAINT sample_versions_standard_same_project
  FOREIGN KEY (standard_version_id, project_id)
  REFERENCES document_versions (id, project_id) ON DELETE RESTRICT;

ALTER TABLE sample_versions
  DROP CONSTRAINT IF EXISTS sample_versions_blueprint_same_project;
ALTER TABLE sample_versions
  ADD CONSTRAINT sample_versions_blueprint_same_project
  FOREIGN KEY (blueprint_version_id, project_id)
  REFERENCES document_versions (id, project_id) ON DELETE RESTRICT;

-- ---------------------------------------------------------------------------
-- batch_items：单元级执行事实（幂等键、尝试、错误分类）
-- ---------------------------------------------------------------------------
--
-- item_key 是**批次内的单元幂等键**。它的唯一约束是「重放相同成功项不增加样本」
-- 的机制：worker 重复投递同一条消息时，INSERT ... ON CONFLICT 会命中同一行，
-- 于是「该单元已完成」这一事实让后续提交变成 no-op（T05 验收项）。
--
-- source_version_ids 记录这个单元**输入**的版本（标准/蓝图），
-- 用于回答「失败项恢复时是否仍用同一快照」——恢复必须沿用同一快照，
-- 改配置要新建批次（§2.6）。

CREATE TABLE IF NOT EXISTS batch_items (
  id BIGSERIAL PRIMARY KEY,
  batch_id BIGINT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  item_key TEXT NOT NULL,
  sample_id BIGINT REFERENCES samples(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'skipped')),
  attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  -- error_class 与 error_message 分开：前者是**可编程的类别**
  --（rate_limited / timeout / invalid_json / truncated / provider_error），
  -- 界面按它给出可操作建议；后者是具体信息，只供展开查看。
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  -- retryable 决定「恢复失败项」是否会重试这一条。
  -- 不可重试的失败（例如 schema 不合法）重复提交只会浪费预算。
  retryable BOOLEAN NOT NULL DEFAULT TRUE,
  -- 成功的条目指向它产出的样本版本；重放时凭它判断「已完成」。
  sample_version_id BIGINT REFERENCES sample_versions(id) ON DELETE SET NULL,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (batch_id, item_key)
);

CREATE INDEX IF NOT EXISTS idx_batch_items_batch_status
  ON batch_items (batch_id, status, id);
-- 「恢复失败项」只取失败且可重试的条目。
CREATE INDEX IF NOT EXISTS idx_batch_items_retryable
  ON batch_items (batch_id, id) WHERE status = 'failed' AND retryable;

-- sample_versions.batch_item_id 的外键在此补上：两表互相引用，
-- 因此必须先建 batch_items 再建这个约束。
ALTER TABLE sample_versions
  DROP CONSTRAINT IF EXISTS sample_versions_batch_item_fk;
ALTER TABLE sample_versions
  ADD CONSTRAINT sample_versions_batch_item_fk
  FOREIGN KEY (batch_item_id) REFERENCES batch_items(id) ON DELETE SET NULL;

-- ---------------------------------------------------------------------------
-- batch_events：事件时间线（R02 的「事件」）
-- ---------------------------------------------------------------------------
--
-- 事件只记**发生了什么**（谁在什么时候把这个批次推进到哪个状态），
-- 不承载统计（统计从 batch_items 聚合）。这样「事件」与「事实」不会互相矛盾：
-- 界面上的进度永远从条目算，事件只解释「为什么变成现在这样」。

CREATE TABLE IF NOT EXISTS batch_events (
  id BIGSERIAL PRIMARY KEY,
  batch_id BIGINT NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  event_type TEXT NOT NULL,
  -- sequence 是**批次内单调递增**的事件序号，用于客户端去重与排序。
  -- 它由插入时计算（MAX+1），因此乱序到达的重复消息不会产生两个序号。
  sequence INTEGER NOT NULL,
  actor_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  detail JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (batch_id, sequence)
);

CREATE INDEX IF NOT EXISTS idx_batch_events_batch
  ON batch_events (batch_id, sequence DESC);
