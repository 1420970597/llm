-- 0039: 一次性默认工作区成员引导（Issue #160 T28 / 3210 完整入口）
--
-- 默认普通账号需要在首次可用时进入默认工作区，否则它能登录却无法创建项目或
-- 查看工作区级页面。但这不是永久管理员策略：管理员显式移除成员后，服务重启
-- 不能悄悄把权限授回去。
--
-- 因此用 (workspace, user) 的一次性标记记录“这个账号已经被引导过”。首次启动
-- 在同一事务中写成员和标记；之后成员关系即使被删除，标记仍保留，不会被重建。
-- 用户被真正删除时标记随之级联删除；如果配置账号被重新创建，它是新的身份，才会
-- 重新走一次首次引导。

CREATE TABLE IF NOT EXISTS workspace_bootstrap_members (
  workspace_id BIGINT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (workspace_id, user_id)
);

COMMENT ON TABLE workspace_bootstrap_members IS
  '一次性默认成员引导标记：保留管理员撤权，避免 API 重启后重新授予 workspace_members。';
