-- 0022: Atelier 工作区、项目与成员作用域（Issue #160 T02）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1、docs/plans/atelier-api-contract.md §2.1。
--
-- 为什么需要这四张表：
--   `datasets` 同时承担「用户目标」「运行配置」「运行状态」「导出源」四个角色，
--   于是「同一个 topic 跑两次不同方案」只能覆盖旧数据，历史无法解释（#160 §1）。
--   Project 是用户目标与预算的作用域；Batch/SampleVersion 才是运行与内容。
--
-- 迁移编号：0022 起（见 atelier-implementation.md §7.1）。只做加法，不改写已应用迁移。

CREATE TABLE IF NOT EXISTS workspaces (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  -- slug 让「默认工作区」在代码里可幂等 upsert，不必靠「第一条记录」这种脆弱约定。
  slug TEXT NOT NULL UNIQUE,
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- workspace_members 的角色取值刻意与 project_members 不同：
--   * workspace admin 管成员与连接（治理），**不默认拥有**项目内容读权；
--   * project owner/reviewer/viewer 才是内容权限（见 T03 的授权矩阵）。
-- 两张表分开而不是用一张「某人在某处有某角色」的表：作用域不同，
-- 合并后很容易写出「workspace admin 顺带读到所有项目正文」的越权。
CREATE TABLE IF NOT EXISTS workspace_members (
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE IF NOT EXISTS projects (
  id BIGSERIAL PRIMARY KEY,
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  name TEXT NOT NULL,
  goal TEXT NOT NULL DEFAULT '',
  -- target_kind 与 goal 一起构成「不可变目标」：运行开始后不能直接切换，
  -- 需要复制为新项目（#160 T10 验收项）。
  target_kind TEXT NOT NULL CHECK (target_kind IN ('sft', 'grpo')),
  -- 项目状态机（atelier-implementation.md §4.2）。archived 只阻止新运行，
  -- 不删批次或发布 —— 所以它不是「删除」，而是这一列的取值。
  status TEXT NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft', 'designed', 'pilot_running', 'pilot_ready',
                      'scaling', 'review', 'candidate', 'published', 'archived')),
  -- 负责人。项目 owner 至少一人，且不能把最后一名 owner 移除（T03）。
  owner_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- n / m / x 与首次试制量属于「设计目标」，与运行快照无关（§2.1）。
  domain_count INTEGER NOT NULL DEFAULT 1 CHECK (domain_count >= 1),
  directions_per_domain INTEGER NOT NULL DEFAULT 1 CHECK (directions_per_domain >= 1),
  questions_per_direction INTEGER NOT NULL DEFAULT 1 CHECK (questions_per_direction >= 1),
  pilot_size INTEGER NOT NULL DEFAULT 1 CHECK (pilot_size BETWEEN 1 AND 100),
  -- 质量目标：接纳数 / 纳入检查的样本版本数（§2.3）。分母固定，不随筛选变化。
  acceptance_rate_target NUMERIC(4, 3) NOT NULL DEFAULT 0.85
    CHECK (acceptance_rate_target >= 0 AND acceptance_rate_target <= 1),
  -- 预算：整数最小货币单位（分），**不用浮点**（§2.4）。
  budget_currency TEXT NOT NULL DEFAULT 'CNY',
  budget_limit_minor BIGINT NOT NULL DEFAULT 0 CHECK (budget_limit_minor >= 0),
  budget_on_exhausted TEXT NOT NULL DEFAULT 'pause' CHECK (budget_on_exhausted IN ('pause', 'stop')),
  -- 乐观锁：任何项目级写操作都带 row_version 检查（契约 §1.4）。
  row_version BIGINT NOT NULL DEFAULT 1,
  -- 旧资产可追溯映射（T30/T31）。此阶段只留字段，不批量导入旧数据。
  legacy_dataset_id BIGINT,
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 项目名在同一工作区内唯一：创建候选/发布时要用到「项目内唯一版本名」的同类语义，
-- 而重名项目会让「用户口头指代某个项目」永远需要额外消歧。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_projects_workspace_name
  ON projects (workspace_id, lower(name));
-- 列表按「最近更新」排序 + 稳定次键 id 翻页（契约 §1.5）。
CREATE INDEX IF NOT EXISTS idx_projects_workspace_updated
  ON projects (workspace_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects (owner_id);
-- 旧 dataset 映射必须是 1:1：一个 dataset 不能映射到两个项目，
-- 否则 T31 的幂等导入无法判断「已导入」。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_projects_legacy_dataset
  ON projects (legacy_dataset_id) WHERE legacy_dataset_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS project_members (
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('owner', 'reviewer', 'viewer')),
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (project_id, user_id)
);

-- 「谁能在哪些项目上做事」是每次请求都要查的路径（T03 要求撤权即时生效，
-- 不能用登录 cookie 里的副本），因此按 user 建索引，避免全表扫成员表。
CREATE INDEX IF NOT EXISTS idx_project_members_user ON project_members (user_id, project_id);
CREATE INDEX IF NOT EXISTS idx_project_members_project_role ON project_members (project_id, role);

-- idempotency_records：命令幂等（契约 §1.3）。
--
-- 为什么在 0022 就建而不是等 T06 的 0026：**创建项目是第一条需要幂等的命令**。
-- 「下一步」按钮双击会提交两次；没有幂等键时唯一的效果就是多出一个同名项目，
-- 而 T10 的验收项明确要求「创建重复点击只建一次」。T06 复用同一张表做批次命令。
--
-- 关键设计：
--   * 主键 (scope, actor_id, idempotency_key) —— 幂等键绑定 actor 与命令，
--     不同用户用同一个 UUID 不会互相命中；
--   * request_digest 区分「同键同请求」（返回原结果）与「同键不同请求」（409）；
--   * **不用 TTL 过期**：短 TTL 会让长时间后的重放重新执行，而这正是要防的。
--     清理属于保留策略（T21/T33），不是幂等语义的一部分。
CREATE TABLE IF NOT EXISTS idempotency_records (
  scope TEXT NOT NULL,
  actor_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  idempotency_key TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  resource_id BIGINT,
  response_status INTEGER NOT NULL DEFAULT 200,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (scope, actor_id, idempotency_key)
);
