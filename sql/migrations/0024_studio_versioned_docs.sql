-- 0024: Atelier 五类版本化文档与乐观锁（Issue #160 T04）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1、§5、docs/plans/atelier-api-contract.md §2.2。
--
-- 五类对象：blueprint（生产蓝图）、coverage（覆盖计划）、standard（思维标准）、
-- quality_policy（质量策略）、mapping（字段映射）。
--
-- 为什么五类共用「一个文档 + 一个版本表」的同构形状，而不是五套手写表：
--   它们的**语义**不同（节点图 / 领域配额 / 步骤 / 规则 / 字段映射），
--   但**生命周期完全相同**：草稿可编辑 → 保存即产生不可变版本 → 版本只读、可读/比较/复制，
--   项目「当前采用」只是一个指针。用户与调用方需要的是同一套乐观锁与版本语义，
--   在这一层保持一致才能避免「蓝图能回滚、标准不能」这类无理由的能力差异。
--   差异集中在 payload 的 typed schema（由 Go 侧校验），不体现在表结构上。
--
-- 与 chain_standard_versions（迁移 0013）的关系：那套表是**按 dataset+domain** 组织的，
--   12 个方向就有 12 条独立标准；本迁移按**项目**组织，一份标准覆盖整个项目。
--   两者并存：旧表继续服务旧流水线，新表服务批次快照。T31 负责解释历史映射。

-- ---------------------------------------------------------------------------
-- 文档头（logical document）：一个项目里同一类文档可以有多个逻辑文档
-- ---------------------------------------------------------------------------
--
-- logical_id 的用途：同一项目可以同时维护「主方案」与「实验方案」两条线，
-- 各自独立版本化。契约 §2.2 的请求体里有 logicalId 字段，默认 "main"。
-- 唯一键 (project_id, kind, logical_id) 保证「同一逻辑文档只有一条头记录」，
-- 而 current_version 是它的「当前采用指针」（§4.1：项目当前采用只是指针，不改旧批次）。

CREATE TABLE IF NOT EXISTS versioned_documents (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN (
    'blueprint', 'coverage', 'standard', 'quality_policy', 'mapping')),
  logical_id TEXT NOT NULL,
  -- current_version 表示「当前采用」的版本号。批次引用自己的快照，不读这个指针，
  -- 因此改指针不会改变任何已提交批次的输入（§4.1）。
  current_version INTEGER NOT NULL DEFAULT 0 CHECK (current_version >= 0),
  -- 乐观锁版本号：每次保存 +1。它与 current_version 分开是因为
  -- 「当前采用哪一版」与「这个头记录被改过几次」是两个不同的问题；
  -- 用同一个数字会让「回滚到旧版本」与「保存新版本」无法区分。
  row_version BIGINT NOT NULL DEFAULT 1,
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (project_id, kind, logical_id)
);

CREATE INDEX IF NOT EXISTS idx_versioned_documents_project
  ON versioned_documents (project_id, kind, logical_id);

-- ---------------------------------------------------------------------------
-- 版本表（不可变）：payload 只追加
-- ---------------------------------------------------------------------------
--
-- 内容 hash 的用途：批次快照记录的是 hash 而不是「版本号」，
-- 因此即使有人误删/重建行，也能发现「快照引用的内容不再一致」（T05 的验收项）。
-- hash 在 Go 侧对规范化 JSON 计算，不依赖 DB 的序列化（DB 的 JSONB 规范化
-- 会重排键，而跨版本/跨环境必须得到同一个 hash）。
--
-- schema_version 与 kind 绑定的取值由 Go 侧校验（例如 blueprint→blueprint.v1）：
-- 放在 DB CHECK 里会让「新增一个 schema 版本」变成一次 DDL 迁移，
-- 而 schema 演进应该只是代码变更 + 数据里的新取值。

CREATE TABLE IF NOT EXISTS document_versions (
  id BIGSERIAL PRIMARY KEY,
  document_id BIGINT NOT NULL REFERENCES versioned_documents(id) ON DELETE CASCADE,
  version INTEGER NOT NULL CHECK (version >= 1),
  -- project_id 冗余存一份：跨项目引用检查（「引用必须同项目」）需要它，
  -- 而只靠 document_id 联表会让每一条校验都多一次 join，且容易漏掉。
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN (
    'blueprint', 'coverage', 'standard', 'quality_policy', 'mapping')),
  schema_version TEXT NOT NULL,
  payload JSONB NOT NULL,
  content_hash TEXT NOT NULL,
  change_reason TEXT NOT NULL DEFAULT '',
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  -- 同一逻辑文档的版本号唯一：并发保存靠这个约束收敛（不是靠应用层读-改-写）。
  UNIQUE (document_id, version)
);

CREATE INDEX IF NOT EXISTS idx_document_versions_project_kind
  ON document_versions (project_id, kind, version DESC);
CREATE INDEX IF NOT EXISTS idx_document_versions_hash
  ON document_versions (content_hash);

-- ---------------------------------------------------------------------------
-- 跨项目引用禁止：references 边表
-- ---------------------------------------------------------------------------
--
-- 蓝图的覆盖/标准/质量/映射节点引用的是**同项目内**的其它文档版本。
-- 契约 §4.1 要求「所有跨对象引用在服务层校验同项目/工作区，关键关联用复合外键
-- 或等效 DB 约束防止串项目」。
--
-- 为什么用边表而不是在 payload 里存 ID 后只在服务层校验：
--   payload 是 JSONB，数据库无法对里面的 ID 建外键；只靠服务层校验时，
--   任何一条绕过服务层的写入（迁移脚本、手工修库、将来新增的写路径）
--   都能造出串项目的引用，而那种数据错误在发布时才会以「引用了别的项目的映射」
--   形式暴露。边表让 reference 可被数据库强制：
--     * document_version_id 与 target_version_id 都必须存在；
--     * 两侧的 project_id 必须相同 —— 用**复合外键**表达，
--       而不是 CHECK 子查询（CHECK 不能跨表）。
--
-- 复合外键需要被引用列上有唯一约束，因此这里先建 (id, project_id) 唯一索引。

CREATE UNIQUE INDEX IF NOT EXISTS uniq_document_versions_id_project
  ON document_versions (id, project_id);

CREATE TABLE IF NOT EXISTS document_version_references (
  id BIGSERIAL PRIMARY KEY,
  document_version_id BIGINT NOT NULL,
  project_id BIGINT NOT NULL,
  -- node_key 是蓝图节点标识（coverage/standard/generation/...），
  -- 或者是该文档类型内部的槽位名。保留它让「哪个节点引用了什么」可查询，
  -- 而不用把 payload 解析出来。
  node_key TEXT NOT NULL,
  target_version_id BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  -- 复合外键：两侧的 project_id 都被强制等于本行的 project_id，
  -- 于是「A 项目的蓝图引用 B 项目的标准」在数据库层无法成立。
  FOREIGN KEY (document_version_id, project_id)
    REFERENCES document_versions (id, project_id) ON DELETE CASCADE,
  FOREIGN KEY (target_version_id, project_id)
    REFERENCES document_versions (id, project_id) ON DELETE CASCADE,
  UNIQUE (document_version_id, node_key, target_version_id)
);

CREATE INDEX IF NOT EXISTS idx_document_version_references_target
  ON document_version_references (target_version_id);
