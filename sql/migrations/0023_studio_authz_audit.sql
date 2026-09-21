-- 0023: Atelier 项目级授权、会话撤权与审计（Issue #160 T03）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1、docs/plans/atelier-api-contract.md §1.6。
--
-- 本迁移只做加法：给既有 audit_logs 补上「审计必须有的字段」。
--
-- 为什么必须补这些字段（T03 验收项原文：审计含 actor/object/revision/reason/requestId）：
--   既有的 audit_logs 只有 actor/action/resource_type/resource_id/detail 五列，
--   其中 resource_id 是 TEXT 且 actor 是自由文本。它无法回答两个项目级问题：
--     1. 「这条审计属于哪个项目/工作区」—— 需要按项目查询与对账（T27 的动态）；
--     2. 「这条变更是针对哪个 revision、以什么理由做的」—— 没有 revision 就无法
--        判断「审计对应的是哪一版内容」，没有 reason 就无法解释用户为什么这么改。
--   没有 request_id 时，用户报错截图与日志无法对上同一次请求。
--
-- 口径（§4.3）：审计里的 revision 是**该对象当时的**乐观锁版本号，
-- 不做投影或聚合；判断冲突检测用 aggregate_review_revision（T16），不是这一列。

ALTER TABLE audit_logs
  ADD COLUMN IF NOT EXISTS workspace_id BIGINT,
  ADD COLUMN IF NOT EXISTS project_id BIGINT,
  ADD COLUMN IF NOT EXISTS actor_user_id BIGINT,
  ADD COLUMN IF NOT EXISTS revision BIGINT,
  ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '';

-- 外键：审计指向项目时必须真实存在，且项目删除后审计也随之消失
--（审计是项目历史的一部分，不是独立存在的合规档案；跨项目泄漏比历史丢失更严重）。
ALTER TABLE audit_logs
  DROP CONSTRAINT IF EXISTS audit_logs_project_fk;
ALTER TABLE audit_logs
  ADD CONSTRAINT audit_logs_project_fk
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE;

ALTER TABLE audit_logs
  DROP CONSTRAINT IF EXISTS audit_logs_workspace_fk;
ALTER TABLE audit_logs
  ADD CONSTRAINT audit_logs_workspace_fk
  FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE;

ALTER TABLE audit_logs
  DROP CONSTRAINT IF EXISTS audit_logs_actor_fk;
ALTER TABLE audit_logs
  ADD CONSTRAINT audit_logs_actor_fk
  FOREIGN KEY (actor_user_id) REFERENCES users(id) ON DELETE SET NULL;

-- 项目动态按时间倒序分页；末位 id 保证全序（契约 §1.5 的稳定游标要求）。
CREATE INDEX IF NOT EXISTS idx_audit_logs_project_time
  ON audit_logs (project_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_workspace_time
  ON audit_logs (workspace_id, created_at DESC, id DESC);
-- 按请求 ID 反查一次请求产生的全部审计（排障与并发分析）。
CREATE INDEX IF NOT EXISTS idx_audit_logs_request
  ON audit_logs (request_id) WHERE request_id <> '';

-- 成员行必须至少有一个人是 owner：数据库层用触发器兜底。
--
-- 为什么不在应用层就够了：T03 的验收项是「不得移除最后一名 owner」，
-- 而移除路径不止一条（DELETE 成员、PATCH 降级角色、workspace 成员连带清理）。
-- 应用层逐个路径加判断必然会漏一条，而且漏掉的那条会产出
-- 「谁都进不去、也没人能加成员」的项目 —— 那是不可恢复的数据损坏。
--
-- 实现为行级触发器：在 project_members 的删除/更新之后检查该项目的 owner 数。
-- 允许「项目正在被删除」：那种情况下 projects 行已不可见，直接放行。
CREATE OR REPLACE FUNCTION studio_assert_project_has_owner() RETURNS TRIGGER AS $$
DECLARE
  target_project BIGINT;
  owner_count INTEGER;
BEGIN
  target_project := COALESCE(NEW.project_id, OLD.project_id);

  -- 项目本身正在被删除（ON DELETE CASCADE 触发的清理）时不检查：
  -- 否则删除项目会因为「删掉最后一个 owner 成员行」而失败。
  IF NOT EXISTS (SELECT 1 FROM projects WHERE id = target_project) THEN
    RETURN COALESCE(NEW, OLD);
  END IF;

  SELECT COUNT(*) INTO owner_count
  FROM project_members
  WHERE project_id = target_project AND role = 'owner';

  IF owner_count = 0 THEN
    RAISE EXCEPTION '项目必须至少保留一名 owner（project_id=%）', target_project
      USING ERRCODE = 'check_violation';
  END IF;

  RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_project_members_keep_owner ON project_members;
CREATE TRIGGER trg_project_members_keep_owner
  AFTER DELETE OR UPDATE OF role ON project_members
  FOR EACH ROW
  EXECUTE FUNCTION studio_assert_project_has_owner();
