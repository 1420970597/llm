package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现工作区成员的读写（Issue #160 T28）。
//
// 契约：#160 T28 的原文要求「团队页实现真实成员添加/移除/角色变更，首版可选择
// 已有账号；项目 owner 与 workspace admin 职责分开」，以及 #160 T03 的
// 「workspace admin 管理成员/连接，**不默认拥有所有项目内容读权**」。
//
// 三条规则（每条都对应一个会被用户感知的坏状态）：
//
//  1. **最后一名 workspace admin 不可移除**（与项目最后一名 owner 同一理由）：
//     移除之后没人能再管理成员与连接，而这种状态只能靠直接改数据库恢复。
//  2. **移除工作区成员时，如果他还是本工作区某个项目的最后一名 owner，
//     则拒绝并列出阻塞的项目**：否则项目会留下一个「没有任何 owner」的状态，
//     而那个项目的发布/判断路径会永久缺一个负责人。
//  3. **移除工作区成员会同时移除他在本工作区的项目成员关系**：保留它们会让
//     界面上显示「已移出团队」而数据仍然可读 —— 那是安全相关的自相矛盾。
//     因此宁可拒绝（规则 2）也不留下半移除状态。

// ErrLastWorkspaceAdmin 表示操作会移除最后一名工作区管理员。
var ErrLastWorkspaceAdmin = errors.New("工作区必须至少保留一名管理员，请先指定另一位管理员后再操作")

// ErrWorkspaceMemberNotBlocked 表示用户不是工作区成员（无需移除）。
var ErrWorkspaceMemberNotBlocked = errors.New("该用户不是本工作区成员")

// ErrWorkspaceMemberManageDenied 表示底层调用者没有目标工作区的治理权限。
// HTTP handler 会在进入 store 前做同一判定；store 仍然重复检查，避免未来
// 的导入、后台任务或新路由把 actor/target workspace 搞混后形成跨工作区写入。
var ErrWorkspaceMemberManageDenied = errors.New("需要目标工作区管理员才能管理成员")

// WorkspaceMember 是工作区成员（带用户邮箱与历史角色，便于界面展示）。
type WorkspaceMember struct {
	UserID    int64  `json:"userId"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	UserRole  string `json:"userRole"`
	CreatedAt string `json:"createdAt"`
}

// BlockingProject 是阻止移除的项目（用户仍是它的最后一名 owner）。
type BlockingProject struct {
	ProjectID int64  `json:"projectId"`
	Name      string `json:"name"`
}

// ErrWorkspaceMemberOwnsProjects 表示该用户仍是某些项目的最后一名 owner。
type ErrWorkspaceMemberOwnsProjects struct {
	Projects []BlockingProject
}

func (err *ErrWorkspaceMemberOwnsProjects) Error() string {
	names := make([]string, 0, len(err.Projects))
	for _, project := range err.Projects {
		names = append(names, project.Name)
	}
	return fmt.Sprintf("该用户仍是这些项目的最后一名负责人：%s。请先在这些项目里指定另一位负责人后再移除",
		strings.Join(names, "、"))
}

// WorkspaceMemberStore 提供工作区成员的读写。
type WorkspaceMemberStore struct {
	db *pgxpool.Pool
}

// NewWorkspaceMemberStore 构造工作区成员 store。
func NewWorkspaceMemberStore(db *pgxpool.Pool) *WorkspaceMemberStore {
	return &WorkspaceMemberStore{db: db}
}

// ListWorkspaceMembers 列出工作区成员。
func (s *WorkspaceMemberStore) ListWorkspaceMembers(ctx context.Context, workspaceID int64) ([]WorkspaceMember, error) {
	rows, err := s.db.Query(ctx, `
    SELECT m.user_id, u.email, m.role, u.role, to_char(m.created_at, 'YYYY-MM-DD"T"HH24:MI:SSOF')
    FROM workspace_members m JOIN users u ON u.id = m.user_id
    WHERE m.workspace_id = $1
    ORDER BY (m.role = 'admin') DESC, m.user_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []WorkspaceMember{}
	for rows.Next() {
		var member WorkspaceMember
		if err := rows.Scan(&member.UserID, &member.Email, &member.Role, &member.UserRole, &member.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// UpsertWorkspaceMember 添加成员或变更其工作区角色。
func (s *WorkspaceMemberStore) UpsertWorkspaceMember(ctx context.Context, workspaceID, actorID, targetUserID int64, role string) (WorkspaceMember, error) {
	if role != model.WorkspaceRoleAdmin && role != model.WorkspaceRoleMember {
		return WorkspaceMember{}, model.FieldErrors{{Field: "role",
			Message: "只能是 admin 或 member"}}
	}
	if targetUserID <= 0 {
		return WorkspaceMember{}, model.FieldErrors{{Field: "userId", Message: "必填"}}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return WorkspaceMember{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 锁住工作区公共行，确保「管理员资格」与后面的成员写入针对同一个
	// workspace 且不会在并发撤权时出现 write-skew。仅允许一个空工作区由
	// actor 把自己初始化为第一名 admin；正常 API 路径仍要求已有 admin。
	if err := ensureWorkspaceAdminTx(ctx, tx, workspaceID, actorID); err != nil {
		var memberCount int
		if errors.Is(err, ErrWorkspaceMemberManageDenied) && role == model.WorkspaceRoleAdmin && actorID == targetUserID {
			if countErr := tx.QueryRow(ctx, `
        SELECT COUNT(*) FROM workspace_members WHERE workspace_id = $1`, workspaceID).Scan(&memberCount); countErr != nil {
				return WorkspaceMember{}, countErr
			}
			if memberCount != 0 {
				return WorkspaceMember{}, err
			}
		} else {
			return WorkspaceMember{}, err
		}
	}

	// 目标用户必须存在：否则会写出一个指向不存在用户的关系行。
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, targetUserID).Scan(&exists); err != nil {
		return WorkspaceMember{}, err
	}
	if !exists {
		return WorkspaceMember{}, model.FieldErrors{{Field: "userId", Message: "用户不存在"}}
	}

	// 降级最后一名管理员同样是不可逆状态：先数一下现有管理员。
	if role == model.WorkspaceRoleMember {
		if err := ensureNotLastWorkspaceAdminTx(ctx, tx, workspaceID, targetUserID); err != nil {
			return WorkspaceMember{}, err
		}
	}

	if _, err := tx.Exec(ctx, `
    INSERT INTO workspace_members (workspace_id, user_id, role, created_by)
    VALUES ($1, $2, $3, $4)
    ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		workspaceID, targetUserID, role, actorID); err != nil {
		return WorkspaceMember{}, err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "workspace_member_upsert", Resource: "workspace_member",
		ResourceID: fmt.Sprint(targetUserID), WorkspaceID: workspaceID,
		Reason: fmt.Sprintf("role=%s", role),
	}); err != nil {
		return WorkspaceMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkspaceMember{}, err
	}
	return s.getWorkspaceMember(ctx, workspaceID, targetUserID)
}

// RemoveWorkspaceMember 移除成员（并清理他在本工作区的项目成员关系）。
func (s *WorkspaceMemberStore) RemoveWorkspaceMember(ctx context.Context, workspaceID, actorID, targetUserID int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureWorkspaceAdminTx(ctx, tx, workspaceID, actorID); err != nil {
		return err
	}

	var isMember bool
	if err := tx.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id = $1 AND user_id = $2)`,
		workspaceID, targetUserID).Scan(&isMember); err != nil {
		return err
	}
	if !isMember {
		return ErrWorkspaceMemberNotBlocked
	}
	if err := ensureNotLastWorkspaceAdminTx(ctx, tx, workspaceID, targetUserID); err != nil {
		return err
	}
	blocking, err := blockingProjectsTx(ctx, tx, workspaceID, targetUserID)
	if err != nil {
		return err
	}
	if len(blocking) > 0 {
		return &ErrWorkspaceMemberOwnsProjects{Projects: blocking}
	}

	if _, err := tx.Exec(ctx, `
    DELETE FROM project_members pm
    USING projects p
    WHERE pm.project_id = p.id AND p.workspace_id = $1 AND pm.user_id = $2`,
		workspaceID, targetUserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
    DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, targetUserID); err != nil {
		return err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: actorID, Action: "workspace_member_removed", Resource: "workspace_member",
		ResourceID: fmt.Sprint(targetUserID), WorkspaceID: workspaceID,
		Reason: "移除工作区成员并清理其在本工作区的项目成员关系",
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ensureWorkspaceAdminTx locks the target workspace and verifies the actor's
// current role. The lock makes the authorization decision and subsequent
// member mutation share one serialized workspace scope.
func ensureWorkspaceAdminTx(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64) error {
	var lockedID int64
	if err := tx.QueryRow(ctx, `
    SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, workspaceID).Scan(&lockedID); err != nil {
		return err
	}
	var role string
	err := tx.QueryRow(ctx, `
    SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, actorID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWorkspaceMemberManageDenied
	}
	if err != nil {
		return err
	}
	if role != model.WorkspaceRoleAdmin {
		return ErrWorkspaceMemberManageDenied
	}
	return nil
}

// ensureNotLastWorkspaceAdminTx 拒绝「移除/降级最后一名工作区管理员」。
func ensureNotLastWorkspaceAdminTx(ctx context.Context, tx pgx.Tx, workspaceID, targetUserID int64) error {
	var isAdmin bool
	if err := tx.QueryRow(ctx, `
    SELECT EXISTS(SELECT 1 FROM workspace_members
      WHERE workspace_id = $1 AND user_id = $2 AND role = '`+model.WorkspaceRoleAdmin+`')`,
		workspaceID, targetUserID).Scan(&isAdmin); err != nil {
		return err
	}
	if !isAdmin {
		return nil
	}
	var adminCount int
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) FROM workspace_members
    WHERE workspace_id = $1 AND role = '`+model.WorkspaceRoleAdmin+`'`, workspaceID).Scan(&adminCount); err != nil {
		return err
	}
	if adminCount <= 1 {
		return ErrLastWorkspaceAdmin
	}
	return nil
}

// blockingProjectsTx 返回「用户是最后一名 owner」的项目。
func blockingProjectsTx(ctx context.Context, tx pgx.Tx, workspaceID, userID int64) ([]BlockingProject, error) {
	rows, err := tx.Query(ctx, `
    SELECT p.id, p.name
    FROM projects p
    JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $2 AND pm.role = 'owner'
    WHERE p.workspace_id = $1
      AND (SELECT COUNT(*) FROM project_members owner
           WHERE owner.project_id = p.id AND owner.role = 'owner') <= 1
    ORDER BY p.id`, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocking := []BlockingProject{}
	for rows.Next() {
		var project BlockingProject
		if err := rows.Scan(&project.ProjectID, &project.Name); err != nil {
			return nil, err
		}
		blocking = append(blocking, project)
	}
	return blocking, rows.Err()
}

// getWorkspaceMember 读取单个成员（返回给调用方做响应）。
func (s *WorkspaceMemberStore) getWorkspaceMember(ctx context.Context, workspaceID, userID int64) (WorkspaceMember, error) {
	var member WorkspaceMember
	err := s.db.QueryRow(ctx, `
    SELECT m.user_id, u.email, m.role, u.role, to_char(m.created_at, 'YYYY-MM-DD"T"HH24:MI:SSOF')
    FROM workspace_members m JOIN users u ON u.id = m.user_id
    WHERE m.workspace_id = $1 AND m.user_id = $2`, workspaceID, userID).
		Scan(&member.UserID, &member.Email, &member.Role, &member.UserRole, &member.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkspaceMember{}, ErrWorkspaceMemberNotBlocked
	}
	if err != nil {
		return WorkspaceMember{}, err
	}
	return member, nil
}

// ResolveUserIDByEmail 按邮箱解析用户（T28「首版可选择已有账号」）。
func (s *WorkspaceMemberStore) ResolveUserIDByEmail(ctx context.Context, email string) (int64, error) {
	trimmed := strings.ToLower(strings.TrimSpace(email))
	if trimmed == "" {
		return 0, model.FieldErrors{{Field: "email", Message: "必填"}}
	}
	var userID int64
	err := s.db.QueryRow(ctx, `SELECT id FROM users WHERE LOWER(email) = $1`, trimmed).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, model.FieldErrors{{Field: "email",
			Message: "没有这个账号：首版只能选择已有账号（邮件邀请尚未接入）"}}
	}
	if err != nil {
		return 0, err
	}
	return userID, nil
}
