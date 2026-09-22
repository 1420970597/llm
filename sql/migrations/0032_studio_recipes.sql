-- 0032: 方案库、方案版本与复制项目（Issue #160 T26）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1（Recipe 对象：「(recipe_id,
-- version)、适用范围、限制」）、§6.3（T26）、#160 T26 的原文要求：
--
--   * `/recipes` 与 `/recipes/{id}`，保存覆盖/标准/蓝图/质量/映射组合、适用类型、
--     限制和可见范围；明确版本发布权限；
--   * 以方案创建项目**复制实际配置**，保留 sourceRecipeVersionId；
--   * 模型连接/成员/凭证不跨工作区复制，缺连接待用户绑定；
--   * 方案升级只影响未来复制。
--
-- 三条用表结构而不是约定保证的性质：
--
--  1. **版本不可变**：`recipe_versions` 只追加（`UNIQUE (recipe_id, version)`），
--     且 `payload` 存的是五类文档的**内容快照**而不是版本 ID 引用。
--     只存 ID 会让「方案作者把蓝图改了」静默改变所有引用该方案的项目 ——
--     而复制出来的项目当初是哪一份配置，是历史事实。
--
--  2. **可见范围**：`visibility ∈ {private, workspace}`。private 只对创建者可见
--     （草稿/未验证的方案不该被同事当成公认做法），workspace 对内可读。
--     共享范围变化必须留审计（T26 验收项），因此这里只存范围，审计走 audit_logs。
--
--  3. **适用类型独立于内容**：`target_kind` 单独一列而不是从 payload 推。
--     SFT 的蓝图节点与 GRPO 的档位配置不是同一套东西，而 payload 里没有
--     一个可靠的「这份方案属于哪一类」字段（蓝图 schema 里没有 target_kind）。
--     从内容推会得到「看起来能推、偶尔猜错」的行为，而错配的方案把 SFT 配置
--     复制进 GRPO 项目会产出一批结构错误的样本。

CREATE TABLE IF NOT EXISTS recipes (
  id BIGSERIAL PRIMARY KEY,
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  -- name_key 是规范化键（小写、去空白）。用规范键做唯一约束：
  -- 否则「医疗问答」与「医疗问答 」（尾随空格）会被当成两个方案，
  -- 而用户在列表里看到的是两行看起来完全一样的记录。
  name_key TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  target_kind TEXT NOT NULL CHECK (target_kind IN ('sft', 'grpo')),
  visibility TEXT NOT NULL DEFAULT 'workspace'
    CHECK (visibility IN ('private', 'workspace')),
  -- 适用场景与限制：面向使用者的说明，展示在方案详情页。
  applicable_scope TEXT NOT NULL DEFAULT '',
  limitations JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (workspace_id, name_key)
);

CREATE INDEX IF NOT EXISTS idx_recipes_workspace
  ON recipes (workspace_id, target_kind, updated_at DESC);

CREATE TABLE IF NOT EXISTS recipe_versions (
  id BIGSERIAL PRIMARY KEY,
  recipe_id BIGINT NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
  -- workspace_id 冗余一份：判定可见性时不必再 join recipes，
  -- 而「跨工作区复制」的检查也因此能在一次查询里完成。
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  version INTEGER NOT NULL CHECK (version > 0),
  status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
  -- payload 是 RecipePayload 的 JSON（五类文档内容快照）。
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,
  content_hash TEXT NOT NULL DEFAULT '',
  change_reason TEXT NOT NULL DEFAULT '',
  published_at TIMESTAMPTZ,
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (recipe_id, version)
);

CREATE INDEX IF NOT EXISTS idx_recipe_versions_recipe
  ON recipe_versions (recipe_id, version DESC);

-- 项目保留来源方案版本（T26：「以方案创建项目复制实际配置，保留 sourceRecipeVersionId」）。
--
-- ON DELETE SET NULL：删掉一个方案版本不该删掉用它建出来的项目；
-- 但链接会断，因此详情页在 source 为空时要如实说明「来源方案已删除」，
-- 而不是显示一个不存在的方案名。
ALTER TABLE projects
  ADD COLUMN IF NOT EXISTS source_recipe_version_id BIGINT
    REFERENCES recipe_versions(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_projects_source_recipe
  ON projects (source_recipe_version_id)
  WHERE source_recipe_version_id IS NOT NULL;

COMMENT ON TABLE recipes IS
  'T26：方案（可复用的配置组合）。版本只追加；applicable_scope/limitations 面向使用者说明。';
COMMENT ON COLUMN recipe_versions.payload IS
  'T26：五类文档（蓝图/覆盖/标准/质量策略/映射）的内容快照。存内容而不是版本 ID 引用 —— 方案作者改内容不应影响已复制出去的项目。';
