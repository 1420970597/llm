-- 0036: Atelier 同基准试制比较（Issue #160 T18）
--
-- 契约来源：docs/plans/atelier-implementation.md §3（P06 试制对比）、
-- docs/plans/atelier-api-contract.md §2.5（实验固定范围）。
--
-- 编号说明：§7.1 的编号表到 0034 截止，0035 已被 T17 的选择快照占用，
-- 本迁移取 0036（同样的理由：不重编号已冻结的 0031–0034）。
--
-- 本迁移要解决的问题（#160 T18 的原文判断）：
--   「不是拿两个任意批次百分比相减」—— 任意两个批次的输入问题、覆盖切片、
--   量表与抽样都可能不同，把它们的「平均分」相减得到的差异主要来自
--   **输入不同**而非方案不同。那类结论看起来很有说服力，但它是错的。
--
-- 因此 baseline 把「比较的前提」冻结成一行不可变记录：
--   固定输入问题版本 + 覆盖切片 + 评价口径（量表/seed/裁判）+ 两侧批次 ID。
--   只有两侧都基于**同一份 baseline**时，报告才允许标注「可比」。

CREATE TABLE IF NOT EXISTS comparison_baselines (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

  -- 冻结的比较前提（全部不可变）。
  --
  -- InputQuestionVersionID 指向被固定下来的输入问题集合的来源版本。
  -- 用版本而不是「当前问题表」：后者会随生成推进而变化，
  -- 于是「两侧用的是同一批问题」这件事无法事后核对。
  input_ref TEXT NOT NULL DEFAULT '',
  coverage_slice JSONB NOT NULL DEFAULT '{}'::jsonb,
  sampling_seed BIGINT NOT NULL DEFAULT 0,
  rubric JSONB NOT NULL DEFAULT '{}'::jsonb,
  judges JSONB NOT NULL DEFAULT '[]'::jsonb,
  -- metric 说明比较的口径：逐题配对（paired）还是覆盖生成本身（coverage）。
  --
  -- 「覆盖生成本身」单列为 coverage：两侧输入**不同**（各自生成覆盖），
  -- 因此不能伪称逐题配对（T18 验收项）。
  metric TEXT NOT NULL DEFAULT 'paired'
    CHECK (metric IN ('paired', 'coverage')),

  -- 两侧批次。用 RESTRICT：批次被删会让「当时比的是什么」无法回答。
  left_batch_id BIGINT REFERENCES batches(id) ON DELETE RESTRICT,
  right_batch_id BIGINT REFERENCES batches(id) ON DELETE RESTRICT,

  name TEXT NOT NULL DEFAULT '',
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- 两侧不能是同一批次：与自己比较得到的差异恒为 0，而那会让用户
  -- 以为「方案没差别」。
  CONSTRAINT comparison_baselines_two_sides CHECK (
    left_batch_id IS NULL OR right_batch_id IS NULL OR left_batch_id <> right_batch_id
  )
);

CREATE INDEX IF NOT EXISTS idx_comparison_baselines_project
  ON comparison_baselines (project_id, created_at DESC, id DESC);

-- ---------------------------------------------------------------------------
-- 采用决定（T18「采用 A/B 只更新项目采用指针并记录依据」）
-- ---------------------------------------------------------------------------
--
-- 为什么单独一张表而不是给 projects 加一列：
--   「采用了哪个方案、依据是什么、谁在什么时候决定的」是**审计事实**，
--   而 projects 上的指针只需要回答「当前用哪个」。两者分开后，
--   指针可以被更新而依据永远保留（只追加）。

CREATE TABLE IF NOT EXISTS comparison_adoptions (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  baseline_id BIGINT NOT NULL REFERENCES comparison_baselines(id) ON DELETE RESTRICT,
  -- 采用的哪一侧。
  adopted_side TEXT NOT NULL CHECK (adopted_side IN ('left', 'right')),
  adopted_batch_id BIGINT NOT NULL REFERENCES batches(id) ON DELETE RESTRICT,
  -- 依据：必须由用户写出理由（与人工判断同一原则：没有理由的结论无法复核）。
  reason TEXT NOT NULL CHECK (length(btrim(reason)) > 0),
  -- 依据引用（配对完成数、维度差异等）的具体数值快照。
  evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_comparison_adoptions_project
  ON comparison_adoptions (project_id, created_at DESC, id DESC);

-- ---------------------------------------------------------------------------
-- 项目采用指针（可被更新的唯一一处）
-- ---------------------------------------------------------------------------
--
-- 只有「当前用哪个方案」是可变的；其余（baseline/依据）都只追加。
-- 这一处可变是因为它表达的正是「当前状态」，而历史由 adoptions 保留。

CREATE TABLE IF NOT EXISTS project_adopted_batches (
  project_id BIGINT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  batch_id BIGINT NOT NULL REFERENCES batches(id) ON DELETE RESTRICT,
  baseline_id BIGINT NOT NULL REFERENCES comparison_baselines(id) ON DELETE RESTRICT,
  adopted_side TEXT NOT NULL CHECK (adopted_side IN ('left', 'right')),
  adoption_id BIGINT NOT NULL REFERENCES comparison_adoptions(id) ON DELETE RESTRICT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
