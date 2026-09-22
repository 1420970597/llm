-- 0030: Atelier 追加式人工判断、分派与冲突协调（Issue #160 T16）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1（Decision 对象）、
-- §4.3（reviewer_revision / aggregate_review_revision / evidence_revision）；
-- docs/plans/atelier-api-contract.md §2.7（记录判断）、§2.8（候选 blocker）。
--
-- 本迁移要解决的问题（#160 T16 的原文判断）：
--   现有处置是**就地更新**的（questions.cleaning_status 之类），于是：
--     * 一次纠错会把「原本判了什么、谁判的、为什么」永久抹掉，
--       而申诉与复核恰恰只需要这些；
--     * 「同一人并发更正」与「两人意见相反」是两件完全不同的事，
--       共用一个字段时无法区分，于是要么误判成冲突、要么静默覆盖；
--     * 证据集变化（补齐实验、发现新风险）后，旧的「已接纳」结论仍然生效，
--       于是**新风险可以沿用旧接纳**发布出去（T16 验收项明确禁止）。
--
-- 三条不可让步的性质，用表结构而不是约定来保证：
--
--  1. **只追加**：review_decisions 没有 UPDATE 路径。更正通过 supersedes
--     指向被取代的那条，旧行永远保留。
--
--  2. **投影由事务递增**：review_projections.aggregate_review_revision 只在
--     「有效判断发生变化」时递增，供 T20 冻结候选时检测竞争
--     （「我确认时看到的是这一版判断吗」）。
--
--  3. **证据版本参与判定**：每条判断记录它确认的 evidence_revision；
--     证据集递增后旧判断不再构成有效接纳（投影回到 pending），
--     而迟到提交会因版本不匹配被拒。

-- ---------------------------------------------------------------------------
-- review_decisions：追加式判断（契约 §2.7）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS review_decisions (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  sample_id BIGINT NOT NULL,
  -- ON DELETE RESTRICT：判断是**历史事实**，不能因为样本被删就消失
  --（那样「谁在什么时候做过什么判断」会随数据清理静默丢失）。
  sample_version_id BIGINT NOT NULL REFERENCES sample_versions(id) ON DELETE RESTRICT,
  -- 内容 hash 冻结在该行：判断的是**这一版内容**，而不是「这个样本」。
  -- 内容产生新版本时需要新判断（T16 验收项）。
  content_hash TEXT NOT NULL DEFAULT '',

  -- 提交时确认的必需证据集版本（契约 §2.7 的 evidenceRevision）。
  evidence_revision BIGINT NOT NULL DEFAULT 0,

  reviewer_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- 该审阅者的**个人**并发序号（契约 §4.3）。
  -- 同一人的两次并发更正用它检查：后到者若拿着旧序号则 409 并保留输入。
  -- 注意这与 aggregate_review_revision 是**两件事**：
  --   个人序号管「我自己有没有被并发覆盖」，
  --   聚合序号管「这一版内容的有效处置变没变」（T20 冻结时用）。
  reviewer_revision BIGINT NOT NULL CHECK (reviewer_revision >= 1),

  action TEXT NOT NULL CHECK (action IN ('accepted', 'quarantined')),
  -- 理由必填：没有理由的判断无法被复核，也无法在冲突时被协调。
  reason TEXT NOT NULL CHECK (length(btrim(reason)) > 0),

  -- 更正指向被取代的判断。自引用 + RESTRICT：不能删掉一条被更正的判断
  --（那会让「当时判了什么」消失，而 supersedes 链正是纠错可追溯的依据）。
  supersedes BIGINT REFERENCES review_decisions(id) ON DELETE RESTRICT,

  -- 协调决定：项目 owner 对「两人意见相反」给出的最终裁定。
  -- 它与普通判断**同一张表**（因此同样只追加、同样有理由），
  -- 但用 resolution_of 标记它是针对某个冲突的裁定。
  resolution_of BIGINT REFERENCES review_decisions(id) ON DELETE RESTRICT,

  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 同一审阅者对同一版本的同一序号只能有一条：重复提交同一序号是幂等重放
-- （网络重试），不是第二次判断。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_review_decision_revision
  ON review_decisions (sample_version_id, reviewer_id, reviewer_revision);

CREATE INDEX IF NOT EXISTS idx_review_decisions_version
  ON review_decisions (sample_version_id, id DESC);

CREATE INDEX IF NOT EXISTS idx_review_decisions_project
  ON review_decisions (project_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_review_decisions_reviewer
  ON review_decisions (project_id, reviewer_id, id DESC);

-- ---------------------------------------------------------------------------
-- review_projections：有效处置的投影（可重建，不是事实来源）
-- ---------------------------------------------------------------------------
--
-- 为什么要有投影表而不是每次聚合查询：
--   * 列表页要按「有效处置」筛选并分页，而聚合查询无法走索引；
--   * T20 冻结候选时要在**一个可串行化的点**上读「这一版内容的有效处置
--     与聚合序号」，投影表就是那个点（行锁）。
--
-- 投影**可以从 decisions 重建**（事实只有 decisions 一张表），
-- 因此它损坏不是灾难 —— 这一点很重要：它意味着投影的更新逻辑即使有 bug，
-- 也可以通过重放修复，而不会丢失判断历史。

CREATE TABLE IF NOT EXISTS review_projections (
  sample_version_id BIGINT PRIMARY KEY REFERENCES sample_versions(id) ON DELETE RESTRICT,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  content_hash TEXT NOT NULL DEFAULT '',

  -- 当前生效的必需证据集版本。与判断里的 evidence_revision 比较：
  -- 不一致说明「证据集变了，需要重新判断」。
  evidence_revision BIGINT NOT NULL DEFAULT 0,
  -- 任何有效判断变化时 +1（T20 冻结时检测竞争）。
  aggregate_review_revision BIGINT NOT NULL DEFAULT 0,

  effective_action TEXT NOT NULL DEFAULT 'pending'
    CHECK (effective_action IN ('pending', 'accepted', 'quarantined', 'conflict')),
  -- 「两人相反」被显式表达为 conflict 而不是「最后一条胜出」。
  conflict BOOLEAN NOT NULL DEFAULT FALSE,
  decision_count INTEGER NOT NULL DEFAULT 0 CHECK (decision_count >= 0),
  -- 为什么是 pending（没有判断 / 证据集变化 / 冲突未协调），
  -- 界面据此告诉用户「下一步该做什么」，而不是只显示一个待定状态。
  pending_reason TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_review_projections_action
  ON review_projections (project_id, effective_action, sample_version_id);

-- ---------------------------------------------------------------------------
-- review_assignments：分派（契约 T16「分派到用户/风险范围」）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS review_assignments (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  sample_id BIGINT NOT NULL,
  -- 分派到**内容版本**：同一题产出新版本后旧分派不再适用
  --（否则「已分派」会掩盖「新版本还没人看」这件事）。
  sample_version_id BIGINT NOT NULL REFERENCES sample_versions(id) ON DELETE RESTRICT,
  -- 同一内容版本上的**重复风险聚合**：同一 risk_key 只保留一条待办，
  -- 否则一条内容被 5 条规则命中就会生成 5 个待办，把队列淹掉。
  risk_key TEXT NOT NULL DEFAULT '',
  assignee_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  assigned_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'cancelled')),
  note TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  resolved_at TIMESTAMPTZ
);

-- 同一 (版本, 风险, 受派人) 只有一个未完成待办：重新分派是**更新**而不是新增，
-- 因此「重新分派不丢记录」—— 旧记录通过 status/assigned_by 变更保留痕迹，
-- 而 note 会累积（见 store 的注释）。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_review_assignment_open
  ON review_assignments (sample_version_id, risk_key, assignee_id)
  WHERE status = 'open';

CREATE INDEX IF NOT EXISTS idx_review_assignments_assignee
  ON review_assignments (project_id, assignee_id, status, id);
