-- 0035: Atelier 大范围选择快照（Issue #160 T17）
--
-- 契约来源：docs/plans/atelier-implementation.md §3.2（URL 参数契约的 `selection`）、
-- #160 T17 原文：「大范围选择用服务端 selection snapshot，禁止把数万 ID 塞 URL；
-- 少量 selection 参数也需重新鉴权」。
--
-- 编号说明：§7.1 的编号分配表在 0034 截止，未给本对象预留编号。
-- 为不重编号已冻结的计划（那会让已写完的任务记录与迁移文件对不上），
-- 本迁移取 **0035**，并在 §1.1 记录该追加。
--
-- 为什么必须有服务端快照而不是把 ID 放进 URL：
--   * 数万 ID 的 URL 会超出浏览器与代理的长度上限，表现为「点了发布什么都没发生」；
--   * URL 里的 ID 列表是**客户端可改的**，而发布范围必须是服务端认可的集合
--     （否则「我选中的」与「实际发布的」可以不一致）；
--   * 快照可以被审计（谁在什么时候选了哪些内容），而 URL 不行。

CREATE TABLE IF NOT EXISTS sample_selection_snapshots (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  -- purpose 说明这份快照是为什么建的（发布候选/质量实验/导出）。
  -- 不同用途的鉴权要求可能不同，而「这份快照是给谁用的」必须可查。
  purpose TEXT NOT NULL DEFAULT 'release'
    CHECK (purpose IN ('release', 'experiment', 'export')),
  -- 生成快照时的筛选条件（用于解释「为什么是这些」）。
  filter JSONB NOT NULL DEFAULT '{}'::jsonb,
  item_count INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  -- 过期时间：快照是**临时选择**，不是业务事实。不过期会让「上周选的」
  -- 在本周仍然能直接用于发布，而那正是「确认范围」要防的事。
  expires_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_selection_snapshots_project
  ON sample_selection_snapshots (project_id, created_at DESC, id DESC);

-- 明细：一行一个内容版本。
--
-- 存**样本版本**而不是样本：发布的是内容版本（同一题的两版内容是两件东西）。
CREATE TABLE IF NOT EXISTS sample_selection_items (
  snapshot_id BIGINT NOT NULL REFERENCES sample_selection_snapshots(id) ON DELETE CASCADE,
  -- RESTRICT：已被选入快照的内容版本不得被静默删除（否则发布范围会悄悄变小）。
  sample_version_id BIGINT NOT NULL REFERENCES sample_versions(id) ON DELETE RESTRICT,
  PRIMARY KEY (snapshot_id, sample_version_id)
);

CREATE INDEX IF NOT EXISTS idx_selection_items_version
  ON sample_selection_items (sample_version_id);
