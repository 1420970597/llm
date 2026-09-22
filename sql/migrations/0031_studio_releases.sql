-- 0031: Atelier 发布候选、门槛与抗并发冻结（Issue #160 T20）
--
-- 契约来源：docs/plans/atelier-implementation.md §2.5（发布名与发布 ID）、
-- §4.2（发布状态机）、§4.3（三个 revision）；docs/plans/atelier-api-contract.md
-- §2.8（创建/修订候选）、§2.9（冻结并发布）。
--
-- 本迁移要解决的问题（#160 T20 的原文判断）：
--   旧导出是「按当前筛选条件跑一次」——于是：
--     * 导出的内容清单是**查询结果**，不是冻结的清单：导出过程中有人隔离了
--       一条样本，最终文件与「确认时看到的」不一致；
--     * 「发布失败重试」会重新分配身份（新的导出记录），于是同一个版本名
--       可能对应两个不同的文件；
--     * 门槛（待审阅/冲突/证据完整性）在导出时**不检查**，因为导出不知道
--       人工判断的存在。
--
-- 三条不可让步的性质：
--
--  1. **身份在候选创建时分配**：candidateId 与 **releaseId** 同事务分配，
--     页面全程用 releaseId。发布失败重试**不换身份**（T20 验收项）。
--
--  2. **清单是具体内容版本，不是筛选条件**（§2.8）：`release_items` 存
--     sample_version_id + content_hash。筛选条件会随数据变化，
--     而「按筛选条件导出」正是上面第一个缺陷的成因。
--
--  3. **冻结抗并发**：冻结事务在**候选行**上加锁，并校验每个内容版本的
--     `aggregate_review_revision` / `evidence_revision` 与确认时一致。
--     仅靠「读一遍再写」会允许「确认与冻结之间有人隔离了一条」漏过 ——
--     而那正是发布最常见的严重事故。

CREATE TABLE IF NOT EXISTS releases (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

  -- **稳定身份**：URL、下载、API 路径、审计只用它（§2.5）。
  -- 表的主键就是它，因此不存在「重新分配」的可能。
  --
  -- release_name 是项目内唯一的**可读版本名**（例如 v1.2），只用于展示与引用。
  -- 两者分开是因为：名字会被用户改（且要唯一），而身份不能变。
  release_name TEXT NOT NULL,
  -- 版本名预留的规范化形式（去空白+小写），用于唯一约束：
  -- 直接用 release_name 会让 'V1.2' 与 'v1.2' 同时存在，而用户会以为是同一版。
  release_name_key TEXT NOT NULL,

  status TEXT NOT NULL DEFAULT 'candidate'
    CHECK (status IN ('candidate', 'blocked', 'building', 'published', 'build_failed')),

  -- 交付语义：用途/限制/来源与保留策略。它们进数据卡，
  -- 且**发布后只读**（后续风险通过独立警告表达，不重写历史文件）。
  intended_use TEXT NOT NULL DEFAULT '',
  limitations JSONB NOT NULL DEFAULT '[]'::jsonb,
  provenance JSONB NOT NULL DEFAULT '{}'::jsonb,

  -- 质量与覆盖摘要（数据卡要能解释结论，即使后来项目数据变了）。
  quality_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
  coverage_summary JSONB NOT NULL DEFAULT '{}'::jsonb,

  created_by BIGINT REFERENCES users(id),
  published_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- 项目内版本名唯一（用规范化键）。
  UNIQUE (project_id, release_name_key)
);

CREATE INDEX IF NOT EXISTS idx_releases_project
  ON releases (project_id, created_at DESC, id DESC);

-- ---------------------------------------------------------------------------
-- release_candidates：候选（可修订，修订不换身份）
-- ---------------------------------------------------------------------------
--
-- 候选与 release 是一对多：一个 release 身份下可以有多次候选修订
--（用户改了范围或映射后重新确认），但**永远指向同一个 releaseId**。

CREATE TABLE IF NOT EXISTS release_candidates (
  id BIGSERIAL PRIMARY KEY,
  release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  -- candidate_revision（§4.3）：每次修订 +1。它是「我确认的是哪一版候选」
  -- 的依据，也是冻结时的竞争检测字段。
  revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),

  mapping_version_id BIGINT REFERENCES document_versions(id) ON DELETE RESTRICT,
  format TEXT NOT NULL DEFAULT 'jsonl',

  -- 门槛检查的**结果快照**：每条 blocker 带可跳转对象（契约 §2.8）。
  -- 保存快照而不是每次重算：报告要能解释「当时为什么被挡住」。
  blockers JSONB NOT NULL DEFAULT '[]'::jsonb,

  -- 冻结时的 revision 确认（§2.9）：候选要求每个内容版本在确认时
  -- 处于这些 revision 上，冻结事务据此检测竞争。
  frozen_at TIMESTAMPTZ,
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_release_candidates_release
  ON release_candidates (release_id, revision DESC, id DESC);

-- ---------------------------------------------------------------------------
-- release_items：不可变的发布清单（具体内容版本）
-- ---------------------------------------------------------------------------
--
-- 这是「冻结」的实体：一旦写入就不再变化（发布后只读）。
-- 存 content_hash 与来源引用，使「发布后改项目/隔离样本不能改旧下载」
-- 成立 —— 文件按这些 hash 生成，而 hash 已经固定。

CREATE TABLE IF NOT EXISTS release_items (
  id BIGSERIAL PRIMARY KEY,
  release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  -- 候选修订时重建清单（先删该 release 的 items 再插），因此这里区分 revision。
  revision BIGINT NOT NULL CHECK (revision >= 1),

  sample_id BIGINT NOT NULL,
  -- RESTRICT：已进入发布清单的内容版本不得被删除（否则旧下载无法复算）。
  sample_version_id BIGINT NOT NULL REFERENCES sample_versions(id) ON DELETE RESTRICT,
  content_hash TEXT NOT NULL DEFAULT '',

  -- 冻结时的来源与判断状态：**不得**用「当前项目版本」冒充样本来源版本
  --（T20 验收项）。因此这里存的是样本版本自带的来源 hash。
  standard_version_id BIGINT,
  standard_content_hash TEXT NOT NULL DEFAULT '',
  blueprint_version_id BIGINT,
  blueprint_content_hash TEXT NOT NULL DEFAULT '',

  -- 冻结时的人工判断与证据 revision：冻结事务校验它们与确认时一致。
  aggregate_review_revision BIGINT NOT NULL DEFAULT 0,
  evidence_revision BIGINT NOT NULL DEFAULT 0,
  effective_action TEXT NOT NULL DEFAULT 'pending',

  -- 缩小范围时必须保留原分母与覆盖损失（§2.3）。
  excluded_reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  UNIQUE (release_id, revision, sample_version_id)
);

CREATE INDEX IF NOT EXISTS idx_release_items_release
  ON release_items (release_id, revision, id);

-- ---------------------------------------------------------------------------
-- release_gates：门槛确认记录（谁在什么时候确认了哪些 blocker）
-- ---------------------------------------------------------------------------
--
-- 为什么不把门槛结果只放在 candidates.blockers：
-- 「谁确认了这一版可以发布」是一次**决定**，而 blockers 是**当时的检查结果**。
-- 分开后可以回答「发布时是谁确认的、他看到的是什么」，而重算只能回答
-- 「现在的检查结果是什么」—— 两者在数据变化后会不同。

CREATE TABLE IF NOT EXISTS release_gates (
  id BIGSERIAL PRIMARY KEY,
  release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  revision BIGINT NOT NULL CHECK (revision >= 1),
  -- 确认时的检查结论：blocker 清单与「是否通过」。
  blockers JSONB NOT NULL DEFAULT '[]'::jsonb,
  passed BOOLEAN NOT NULL DEFAULT FALSE,
  -- 缩小范围时的确认：记录原范围与排除数量，使覆盖损失可解释。
  original_item_count INTEGER NOT NULL DEFAULT 0 CHECK (original_item_count >= 0),
  excluded_item_count INTEGER NOT NULL DEFAULT 0 CHECK (excluded_item_count >= 0),
  reason TEXT NOT NULL DEFAULT '',
  confirmed_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_release_gates_release
  ON release_gates (release_id, revision DESC, id DESC);
