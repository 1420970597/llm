-- 0020: generation_runs 活跃记录唯一约束（issue #9）
--
-- 不变量：同一 (dataset_id, stage) 同时只能存在一条活跃记录。
-- 「活跃」的判定与 GenerationRunStore.ActiveRun 完全一致：
-- status IN ('pending', 'running')。
--
-- 为什么唯一键里不能带 status：既有非唯一索引 idx_generation_runs_lookup 把
-- status 放进索引列，即使改成 UNIQUE (dataset_id, stage, status) 也拦不住重复 ——
-- 'pending' 与 'running' 是不同值，两条都能存活。因此必须用**部分唯一索引**，
-- 只约束活跃态，终态（completed / failed / partial_failed）不受限。
--
-- 幂等性：本迁移必须能在「已存在重复活跃记录」的历史库（线上实测有 11 条孤儿
-- running）上成功执行。唯一索引会直接拒绝这类数据，所以必须先处理多余记录，
-- 再建索引；两步都写成可重复执行的形式。
--
-- 为什么是「终结」而不是「删除」多余记录：
--   * 这些记录携带**真实的断点游标**（并发情况下两条记录都可能被各自的请求写进
--     SaveCursor 进度），删除不可逆，会让已完成的领域丢失；
--   * 项目准则禁止无价值确认的破坏性数据操作。
--   因此保留最新一条（与 ActiveRun 的 ORDER BY id DESC 语义一致）作为活跃记录，
--   其余转为 failed 并写入来源说明，用户仍可通过「续跑」把它们跑完。
--   若运维确认这些记录无价值，可在人工复核后用精确条件清理（本迁移不执行删除）：
--     DELETE FROM generation_runs
--     WHERE error_summary LIKE '并发 StartRun 产生的重复活跃记录%';

-- 第 1 步：终结同一 (dataset_id, stage) 下除 id 最大者以外的活跃记录。
--         子查询在 UPDATE 的语句快照上求值，因此 MAX(id) 稳定指向保留项。
UPDATE generation_runs AS target
SET status = 'failed',
    error_summary = '并发 StartRun 产生的重复活跃记录，已由迁移 0020 终结：'
                    || '同阶段保留 id 最大的一条作为活跃记录，'
                    || '本行转为 failed 以恢复「同一阶段仅一条活跃记录」的不变量',
    finished_at = COALESCE(target.finished_at, NOW()),
    updated_at = NOW()
WHERE target.status IN ('pending', 'running')
  AND EXISTS (
    SELECT 1
    FROM generation_runs AS other
    WHERE other.dataset_id = target.dataset_id
      AND other.stage = target.stage
      AND other.status IN ('pending', 'running')
      AND other.id > target.id
  );

-- 第 2 步：建立部分唯一索引，使数据库层（而非调用方的 read-then-insert）成为
--         唯一的仲裁者。GenerationRunStore.StartRun 的 ON CONFLICT 判定依赖它。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_generation_runs_active
  ON generation_runs (dataset_id, stage)
  WHERE status IN ('pending', 'running');
