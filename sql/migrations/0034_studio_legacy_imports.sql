-- 0034: 旧数据幂等导入台账（Issue #160 T31）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1（LegacyImport 对象：
-- 「来源键、内容 hash、游标」）、§6.3（T31）、#160 T31 的原文要求：
--
--   * 按 dataset 小批导入，`legacy_imports` 唯一来源键/游标/内容 hash，
--     支持暂停、续跑、重复执行；
--   * 不覆盖已迁移后产生的新版本；
--   * 迁移前后数量/状态/内容 hash/引用/文件下载对账，失败报告精确到对象。
--
-- 三条用表结构而不是约定保证的性质：
--
--  1. **唯一来源键**：`UNIQUE (source_kind, source_key)`。重复执行时先命中
--     这一行，从而做到「零重复导入」—— 靠「记得先查一下」不算保证。
--
--  2. **游标 + 计数**：`cursor` 是「已处理到的源对象 ID」，计数分列
--     （导入/已存在跳过/无内容跳过/失败）。分列而不是只记一个「处理了 N 条」：
--     「跳过」与「导入」是完全不同的事实，合并计数会让「重复执行」
--     看起来像「又导了一遍」。
--
--  3. **对账快照**：`before_snapshot` / `after_snapshot` 存迁移前后的
--     数量与内容摘要。没有它，迁移完成后「到底有没有丢东西」只能靠人工比对，
--     而人工比对在几千条以上必然变成抽样。
--
-- 关于 status 的取值：`paused` 是**一等状态**而不是「running 的变体」。
-- T31 明确要求「支持暂停、续跑」；把它做成 running 的子状态会让
-- 「现在到底要不要等它」无法从表上判断。

CREATE TABLE IF NOT EXISTS legacy_imports (
  id BIGSERIAL PRIMARY KEY,

  -- 来源类型/键：目前只有 dataset，保留 source_kind 是为了将来导入
  -- 「独立的工件目录」时不用改唯一键的语义。
  source_kind TEXT NOT NULL DEFAULT 'dataset'
    CHECK (source_kind IN ('dataset')),
  source_key TEXT NOT NULL,

  -- 目标项目与批次：**批次可空**（只建项目、内容尚未导入时）。
  -- ON DELETE SET NULL 而不是 CASCADE：台账是审计记录，
  -- 删项目不该把「曾经导入过什么」一起抹掉。
  target_project_id BIGINT REFERENCES projects(id) ON DELETE SET NULL,
  batch_id BIGINT REFERENCES batches(id) ON DELETE SET NULL,

  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'running', 'paused', 'completed', 'failed')),

  -- 已处理到的源对象 ID（当前是 questions.id）。续跑从它之后继续。
  cursor BIGINT NOT NULL DEFAULT 0,
  -- 导入内容的联合摘要：用于对账「同一份源数据是否得到同一份结果」。
  content_hash TEXT NOT NULL DEFAULT '',

  -- 分列计数（见文件头：跳过与导入是不同的事实）。
  source_items INTEGER NOT NULL DEFAULT 0 CHECK (source_items >= 0),
  imported_versions INTEGER NOT NULL DEFAULT 0 CHECK (imported_versions >= 0),
  skipped_existing INTEGER NOT NULL DEFAULT 0 CHECK (skipped_existing >= 0),
  skipped_no_content INTEGER NOT NULL DEFAULT 0 CHECK (skipped_no_content >= 0),
  failed_items INTEGER NOT NULL DEFAULT 0 CHECK (failed_items >= 0),

  -- 对账快照与失败明细（报告精确到对象）。
  before_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
  after_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
  failures JSONB NOT NULL DEFAULT '[]'::jsonb,
  error_message TEXT NOT NULL DEFAULT '',

  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  UNIQUE (source_kind, source_key)
);

CREATE INDEX IF NOT EXISTS idx_legacy_imports_status
  ON legacy_imports (status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_legacy_imports_project
  ON legacy_imports (target_project_id)
  WHERE target_project_id IS NOT NULL;

-- 项目上的来源映射：`projects.legacy_dataset_id` 已由迁移 0022 建立，
-- 这里补一个索引，使「按旧 dataset 反查项目」走索引而不是全表扫描
--（旧路由兼容要按它跳转，见 T31 的「旧 /console/tasks/:id 经映射跳转」）。
CREATE INDEX IF NOT EXISTS idx_projects_legacy_dataset
  ON projects (legacy_dataset_id)
  WHERE legacy_dataset_id IS NOT NULL;

COMMENT ON TABLE legacy_imports IS
  'T31：旧数据导入台账（唯一来源键 + 游标 + 分列计数 + 前后对账快照）。重复执行命中唯一键，不产生重复导入。';
