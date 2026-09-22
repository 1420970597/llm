-- 0033: 动态、未读水位与评论（Issue #160 T27）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1（Activity / Comment：
-- 「事件 ID、评论修订」「已读水位」）、§6.3（T27）、#160 T27 的原文要求：
--
--   * 动态读取**持久化事件**、分页、未读位置；「全部已读」只更新当前用户阅读水位；
--   * 评论锚定样本版本或批次、编辑保留修订、提及只通知**有权访问**的成员；
--     评论不是 Decision，不能解除发布门槛；
--   * 先用带游标增量轮询（SSE 属后续优化），不另建消息平台。
--
-- 两条用表结构而不是约定保证的性质：
--
--  1. **未读是「个人水位」而不是「事件上的已读标记」**。`activity_reads` 每个
--     (user, workspace) 一行，只存「我看到哪了」。反过来的设计（给每个事件
--     记每个用户的已读）会让事件数 × 成员数成为行数，而且新成员加入时
--     需要回填历史事件。水位还有一个语义优势：**未读不等于业务已处理**
--     （T27 验收项）—— 已读只影响红点，不影响任何业务状态。
--
--  2. **评论只追加 + 修订链**。同一作者对同一锚点的更正写成新行并把旧行标为
--     `superseded_by`，因此「当时说了什么」不会被抹掉（与 review_decisions
--     同一原则）。评论**不是** Decision：它不写 review_decisions、不改投影、
--     不参与发布门槛 —— 这一条由代码路径保证（评论 store 不触碰那些表）。
--
-- 动态**不另建事件表**：直接读取既有的 `batch_events` 与 `audit_logs`。
-- 另建一张事件表意味着每条现有写路径都要多写一行，而漏写一处的表现是
-- 「某类事件在动态里永远不出现」——那种缺口不会报错，只会让用户以为没发生。

CREATE TABLE IF NOT EXISTS activity_reads (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  -- 水位：已读到的最大游标（时间 + 事件 ID）。用两个字段而不是一个合成值，
  -- 是为了让「同一毫秒内的多个事件」也能被正确比较（与 contract.Cursor 同构）。
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_event_id BIGINT NOT NULL DEFAULT 0 CHECK (last_seen_event_id >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (user_id, workspace_id)
);

CREATE TABLE IF NOT EXISTS comments (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

  -- 锚点：样本版本或批次（T27 原文「评论锚定样本版本或批次」）。
  -- 用 (kind, id) 而不是两个可空列：两个可空列会让「都为空」或「都非空」
  -- 成为可能，而那种行没有锚点、也就无法被任何页面展示。
  anchor_kind TEXT NOT NULL CHECK (anchor_kind IN ('sample_version', 'batch')),
  anchor_id BIGINT NOT NULL,

  body TEXT NOT NULL,
  -- mentions 是被提及的用户 ID 数组（服务端写入前校验其为项目成员；
  -- 读取时再按**当前**成员关系过滤，因此撤权后提及不再可见）。
  mentions JSONB NOT NULL DEFAULT '[]'::jsonb,

  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
  -- 修订链：新行指回被它取代的行，旧行由 superseded_by 指回新行。
  -- 两个方向都记是刻意的：只记一个方向时，「这条评论是否还有效」
  -- 需要反查全表（而反查在分页与索引下很容易漏行）。
  --
  -- **DEFERRABLE INITIALLY DEFERRED 是必需的**（实测踩过）：
  -- 更正的正确顺序是「给新行预分配 id → 标记旧行 superseded_by = 新 id → 插入新行」
  --（顺序反过来会撞下方的部分唯一索引）。因此标记旧行的那一刻，新行还不存在，
  -- 立即检查的外键会报 23503。推迟到提交时检查，既保住了引用完整性，
  -- 又让「先标记后插入」这个唯一正确的顺序成立。
  supersedes_id BIGINT REFERENCES comments(id) ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED,
  superseded_by BIGINT REFERENCES comments(id) ON DELETE SET NULL DEFERRABLE INITIALLY DEFERRED,

  author_id BIGINT NOT NULL REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 「当前有效评论」的部分唯一索引：同一作者对同一锚点同时只有一条有效评论。
-- 更正（插入新行 + 标记旧行）不会撞唯一约束，因为旧行已被排除在索引之外。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_comment_current
  ON comments (project_id, anchor_kind, anchor_id, author_id)
  WHERE superseded_by IS NULL;

CREATE INDEX IF NOT EXISTS idx_comments_anchor
  ON comments (project_id, anchor_kind, anchor_id, created_at DESC);

-- 动态读取走这两个索引（按时间倒序、带项目作用域）。
CREATE INDEX IF NOT EXISTS idx_batch_events_feed
  ON batch_events (created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_audit_logs_feed
  ON audit_logs (created_at DESC, id DESC)
  WHERE project_id IS NOT NULL;

COMMENT ON TABLE activity_reads IS
  'T27：每个 (user, workspace) 一行的未读水位。已读只影响红点，不改变任何业务状态（未读 != 业务已处理）。';
COMMENT ON TABLE comments IS
  'T27：锚定样本版本或批次的评论。只追加 + 修订链；不是 Decision，不参与发布门槛。';
