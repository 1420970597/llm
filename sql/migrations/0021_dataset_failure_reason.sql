-- 0021: datasets 增加失败原因字段（issue #83）
--
-- 为什么需要它：
-- 此前流水线失败时，`datasets.status` 只会变成 `reasoning_failed` 这类终态，
-- 而**失败原因只出现在 worker 日志里**。前端能拿到的信息只有状态字符串，
-- 于是界面只能显示「答案生成失败 / 系统同步中 / 请排查失败原因」——
-- 用户既不知道真实原因，也不知道去哪修。
--
-- 具体触发本字段的那次故障：全新部署下 `storage_profiles` 表为空，
-- 而答案/评分/导出三个阶段都要写对象存储，于是答案阶段必然失败，
-- 日志里只有英文的 `no rows in result set`。
--
-- 设计取舍：
--   * 只存**最后一个**失败原因（单列），不做失败历史表。
--     理由是用户要回答的问题是「现在为什么失败、我该做什么」，
--     而完整历史已经由 `generation_runs.error_summary`（迁移 0016）按阶段保留。
--     加一张历史表会引入与 generation_runs 的职责重叠。
--   * NOT NULL DEFAULT ''：让既有行与所有 INSERT 语句无需改动即可继续工作
--    （空串表示「无失败原因」），避免为了一个可空字段去改所有写库路径。
--   * 成功推进时由 worker 清空，避免旧的失败原因残留误导用户。
ALTER TABLE datasets
  ADD COLUMN IF NOT EXISTS failure_reason TEXT NOT NULL DEFAULT '';

-- 便于按「有失败原因」筛选待处理任务；部分索引，不索引绝大多数空值行。
CREATE INDEX IF NOT EXISTS idx_datasets_failure_reason
  ON datasets (id)
  WHERE failure_reason <> '';
