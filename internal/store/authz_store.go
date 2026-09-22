package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现 Atelier 项目级授权与审计（Issue #160 T03）。
//
// 契约：docs/plans/atelier-implementation.md §4.1、docs/plans/atelier-api-contract.md §1.6。
//
// 三个必须成立的性质：
//  1. **撤权即时生效**：角色从服务端当前状态判定，不信任登录 cookie 里的副本。
//     会话 cookie 里的 users.role 只是历史角色，成员变更后它不会变化。
//  2. **workspace admin 不默认拥有项目内容读权**：治理角色与内容角色分表。
//  3. **审计与变更同事务**：不允许「写成功但永久无审计」。
//
// 本文件不定义 HTTP 状态码：授权结果是**对象能力**（grant/deny + 原因），
// 由 handler 决定映射成 403 还是资源隐藏型 404（契约 §1.2 要求区分二者）。

// AuthzAction 是需要授权判定的动作。
//
// 用枚举而不是布尔参数：`RequireProject(ctx, id, user, canWrite)` 这种签名
// 在 20 个调用点上会迅速退化成「传 true 就放行」，而枚举让每个调用点
// 显式声明意图，也让审计日志能记录被拒的具体动作。
type AuthzAction string

const (
	// AuthzRead 读取项目内容（版本、批次、样本、证据）。
	AuthzRead AuthzAction = "read"
	// AuthzDesign 设计：保存蓝图/覆盖/标准/质量/映射版本。
	AuthzDesign AuthzAction = "design"
	// AuthzRun 运行：创建批次、暂停/恢复/重试。
	AuthzRun AuthzAction = "run"
	// AuthzReview 判断：提交人工 Decision、分派。
	AuthzReview AuthzAction = "review"
	// AuthzPublish 发布：创建候选、冻结发布、创建下一版。
	AuthzPublish AuthzAction = "publish"
	// AuthzDownload 下载已发布制品。
	AuthzDownload AuthzAction = "download"
	// AuthzManageMembers 管理项目成员。
	AuthzManageMembers AuthzAction = "manage_members"
	// AuthzManageWorkspace 管理工作区治理（成员、连接）。
	AuthzManageWorkspace AuthzAction = "manage_workspace"
)

// AuthzDecision 是一次授权判定的结果。
//
// 为什么返回结构体而不是 error：拒绝有**两种**语义完全不同的情形
// （「不是成员」→ 资源隐藏型 404；「是成员但角色不够」→ 403），
// 把两者压成一个 error 会让调用点只能选一个，而选错就是信息泄漏或误导用户。
type AuthzDecision struct {
	Allowed bool
	// IsMember 表示请求者在项目里是否有成员关系。
	// false 时调用方必须返回**资源隐藏型 404**（不是 403）：
	// 对非成员说「你没权限」等于确认了「这个项目存在」。
	IsMember bool
	// Role 是判定时读取的服务端当前角色（不是会话副本）。
	Role string
	// Project 是判定时读取的项目，避免调用方再查一次（那会引入 TOCTOU 窗口：
	// 判定用的是旧状态、写入用的是新状态）。
	Project model.Project
	// WorkspaceRole 是请求者在项目所属工作区的治理角色（可能为空）。
	WorkspaceRole string
}

// Reason 给出拒绝的中文可操作说明。
func (d AuthzDecision) Reason(action AuthzAction) string {
	if !d.IsMember {
		return "未找到该项目，请返回项目列表确认它是否已被删除或你没有访问权限"
	}
	if d.Project.IsArchived() {
		return "该项目已归档，只能查看历史与下载已发布版本；如需继续请先取消归档"
	}
	switch action {
	case AuthzDesign:
		return "只有项目负责人可以修改设计（蓝图、覆盖、标准、质量与映射版本）"
	case AuthzRun:
		return "只有项目负责人可以发起批次、暂停或恢复运行"
	case AuthzReview:
		return "只有项目负责人与质量审阅者可以提交判断"
	case AuthzPublish:
		return "只有项目负责人可以创建候选与发布"
	case AuthzManageMembers:
		return "只有项目负责人可以管理项目成员"
	case AuthzDownload:
		return "你没有下载该发布的权限"
	default:
		return "你没有执行该操作的权限"
	}
}

// AuthzStore 是项目级授权的唯一判定入口。
//
// 它内嵌 ProjectStore 而不是复制其查询：两个 store 各写一份「角色怎么读」
// 必然会漂移，而权限漂移的后果是越权。
type AuthzStore struct {
	db       *pgxpool.Pool
	projects *ProjectStore
}

func NewAuthzStore(db *pgxpool.Pool) *AuthzStore {
	return &AuthzStore{db: db, projects: NewProjectStore(db)}
}

// ProjectStore 暴露底层项目 store，便于 handler 复用同一份实现。
func (s *AuthzStore) ProjectStore() *ProjectStore { return s.projects }

// AuthorizeProject 读取项目与当前用户的**服务端**角色并判定动作。
//
// 关键点：每次都重新查库（project_members + projects.status）。
// 不读会话 cookie 里缓存的角色 —— T03 的验收项明确要求「成员/角色变更从服务端
// 当前状态判定」，而登录时写入 cookie 的副本在撤权后仍然是旧值。
//
// 判定顺序刻意是「先成员、后角色」：非成员要能返回 IsMember=false，
// 让调用方区分 404 与 403。先查角色再查项目会让非成员拿到「项目不存在」之外的结论。
func (s *AuthzStore) AuthorizeProject(ctx context.Context, projectID, userID int64, action AuthzAction) (AuthzDecision, error) {
	if projectID <= 0 || userID <= 0 {
		return AuthzDecision{}, nil
	}

	project, err := s.projects.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 项目不存在与非成员对调用方**不可区分**，这是有意的：
			// 两者都必须映射成资源隐藏型 404。
			return AuthzDecision{}, nil
		}
		return AuthzDecision{}, err
	}

	role, isMember, err := s.projects.ProjectRole(ctx, projectID, userID)
	if err != nil {
		return AuthzDecision{}, err
	}
	workspaceRole, _, err := s.projects.WorkspaceRole(ctx, project.WorkspaceID, userID)
	if err != nil {
		return AuthzDecision{}, err
	}

	decision := AuthzDecision{
		IsMember:      isMember,
		Role:          role,
		Project:       project,
		WorkspaceRole: workspaceRole,
	}
	if !isMember {
		return decision, nil
	}

	// 归档只阻止**新运行与写操作**，读与下载保持可用（§4.2）。
	if project.IsArchived() {
		decision.Allowed = action == AuthzRead || action == AuthzDownload
		return decision, nil
	}

	switch action {
	case AuthzRead, AuthzDownload:
		// 三种内容角色都能读授权内容；下载的额外限制（「仅允许下载的发布」）
		// 由发布对象自身的可见范围决定，不在项目层判定。
		decision.Allowed = true
	case AuthzDesign, AuthzRun, AuthzPublish, AuthzManageMembers:
		decision.Allowed = role == model.ProjectRoleOwner
	case AuthzReview:
		decision.Allowed = role == model.ProjectRoleOwner || role == model.ProjectRoleReviewer
	case AuthzManageWorkspace:
		// 工作区治理与项目内容分开：workspace admin 管成员/连接，
		// 但项目的写动作仍然要求项目 owner。
		decision.Allowed = workspaceRole == model.WorkspaceRoleAdmin
	default:
		decision.Allowed = false
	}
	return decision, nil
}

// AuthorizeWorkspace 判定工作区治理动作。
//
// 与 AuthorizeProject 分开的原因：治理动作的作用域是 workspace，
// 把它塞进项目判定会让「workspace admin 顺带获得项目写权限」这种越权
// 只需要一个 if 分支就会发生。
func (s *AuthzStore) AuthorizeWorkspace(ctx context.Context, workspaceID, userID int64, action AuthzAction) (AuthzDecision, error) {
	if workspaceID <= 0 || userID <= 0 {
		return AuthzDecision{}, nil
	}
	role, isMember, err := s.projects.WorkspaceRole(ctx, workspaceID, userID)
	if err != nil {
		return AuthzDecision{}, err
	}
	decision := AuthzDecision{IsMember: isMember, WorkspaceRole: role}
	if !isMember {
		return decision, nil
	}
	switch action {
	case AuthzRead:
		// 工作区成员都能读治理信息（成员列表、连接的非秘密标识）。
		decision.Allowed = true
	case AuthzManageWorkspace, AuthzManageMembers:
		decision.Allowed = role == model.WorkspaceRoleAdmin
	default:
		decision.Allowed = false
	}
	return decision, nil
}

// ---------------------------------------------------------------------------
// 成员管理
// ---------------------------------------------------------------------------

// ErrLastOwner 表示操作会移除项目最后一名 owner。
//
// 单独一个哨兵错误而不是靠数据库触发器报错：数据库触发器是最后防线，
// 但它的报错是 SQLSTATE + 中文异常文本，用户看不懂「该怎么做」。
// 应用层先拦住能给出「请先指定另一位负责人」这种可操作提示。
var ErrLastOwner = errors.New("项目必须至少保留一名 owner，请先把另一位成员设为负责人后再操作")

// UpsertProjectMember 添加或修改项目成员角色（T03，T28 的 UI 调用它）。
//
// 同一事务内写审计：成员变更是最需要审计的操作（谁在什么时候给了谁什么权限），
// 而「变更成功但无审计」在事后无法补。
func (s *AuthzStore) UpsertProjectMember(ctx context.Context, projectID, actorID, targetUserID int64, role string, reason, requestID string) error {
	if role != model.ProjectRoleOwner && role != model.ProjectRoleReviewer && role != model.ProjectRoleViewer {
		return &apiStoreError{Message: "项目角色只能是 owner、reviewer 或 viewer"}
	}
	if targetUserID <= 0 {
		return &apiStoreError{Message: "请选择要添加的成员"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 串行化同一项目的**成员变更**。
	//
	// 必须在**公共行**上加锁（项目行），而不是只锁目标成员行：
	// 「至少留一名 owner」是一个**跨行**不变量，而两个不同 owner 的并发降级
	// 会锁到**不同**的成员行 —— 于是两边都数到 2 个 owner、都通过检查、
	// 同时降级，项目变成**零 owner**（谁也无法再管理它）。
	//
	// 这是一次真实缺陷的修复（由 `TestAuthzLastOwnerCheckIsRaceSafe` 在
	// 全量并行负载下发现；孤立运行时不复现，因为窗口很窄）：
	// 原先的注释写的是「事务内检查可防 write-skew」，但机制上做不到 ——
	// 事务内的**读**只保证读到的是一致快照，不保证别的并发事务不写。
	if _, err := tx.Exec(ctx, `
    SELECT id FROM projects WHERE id = $1 FOR UPDATE`, projectID); err != nil {
		return err
	}

	var currentRole string
	err = tx.QueryRow(ctx, `
    SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2 FOR UPDATE`,
		projectID, targetUserID).Scan(&currentRole)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		currentRole = ""
	case err != nil:
		return err
	}

	if currentRole == model.ProjectRoleOwner && role != model.ProjectRoleOwner {
		var ownerCount int
		if err := tx.QueryRow(ctx, `
      SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND role = 'owner'`,
			projectID).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			return ErrLastOwner
		}
	}

	if _, err := tx.Exec(ctx, `
    INSERT INTO project_members (project_id, user_id, role, created_by)
    VALUES ($1, $2, $3, $4)
    ON CONFLICT (project_id, user_id)
    DO UPDATE SET role = EXCLUDED.role, updated_at = NOW()`,
		projectID, targetUserID, role, actorID); err != nil {
		return err
	}

	project, err := projectForAuditTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:     actorID,
		Action:      "member_upsert",
		Resource:    "project_member",
		ResourceID:  strconv.FormatInt(targetUserID, 10),
		WorkspaceID: project.WorkspaceID,
		ProjectID:   projectID,
		Reason:      reason,
		RequestID:   requestID,
		Detail:      fmt.Sprintf("role=%s previous=%s", role, currentRole),
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RemoveProjectMember 移除项目成员。
//
// 拒绝移除最后一名 owner，与 UpsertProjectMember 同样在事务内检查。
// 成员不存在时返回 pgx.ErrNoRows 语义的错误，让 handler 返回 404。
func (s *AuthzStore) RemoveProjectMember(ctx context.Context, projectID, actorID, targetUserID int64, reason, requestID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var role string
	if err := tx.QueryRow(ctx, `
    SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2 FOR UPDATE`,
		projectID, targetUserID).Scan(&role); err != nil {
		return err
	}

	if role == model.ProjectRoleOwner {
		var ownerCount int
		if err := tx.QueryRow(ctx, `
      SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND role = 'owner'`,
			projectID).Scan(&ownerCount); err != nil {
			return err
		}
		if ownerCount <= 1 {
			return ErrLastOwner
		}
	}

	if _, err := tx.Exec(ctx, `
    DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, targetUserID); err != nil {
		return err
	}

	project, err := projectForAuditTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID:     actorID,
		Action:      "member_remove",
		Resource:    "project_member",
		ResourceID:  strconv.FormatInt(targetUserID, 10),
		WorkspaceID: project.WorkspaceID,
		ProjectID:   projectID,
		Reason:      reason,
		RequestID:   requestID,
		Detail:      "role=" + role,
	}); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// 审计
// ---------------------------------------------------------------------------

// StudioAudit 是一条项目级审计记录（T03 验收项：actor/object/revision/reason/requestId）。
type StudioAudit struct {
	ActorID     int64
	Action      string
	Resource    string
	ResourceID  string
	WorkspaceID int64
	ProjectID   int64
	Revision    int64
	Reason      string
	RequestID   string
	Detail      string
}

// WriteStudioAudit 独立写一条审计（调用方没有自己的事务时使用）。
//
// 有事务的调用方必须用 WriteStudioAuditTx：审计与变更同事务才满足
// 「变更与审计同事务或 outbox」（T03 验收项），否则崩溃窗口会留下无审计的变更。
func (s *AuthzStore) WriteStudioAudit(ctx context.Context, entry StudioAudit) error {
	return writeStudioAuditTx(ctx, s.db, entry)
}

// writeStudioAuditTx 在给定事务（或连接池）上写审计。
//
// 接受 pgx.Tx 与 pgxpool.Pool 的公共子集：两者都实现 Exec，
// 用一个最小的本地接口表达，避免为「有事务」和「没事务」写两份 SQL。
func writeStudioAuditTx(ctx context.Context, execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, entry StudioAudit) error {
	var revision *int64
	if entry.Revision > 0 {
		revision = &entry.Revision
	}
	var projectID *int64
	if entry.ProjectID > 0 {
		projectID = &entry.ProjectID
	}
	var workspaceID *int64
	if entry.WorkspaceID > 0 {
		workspaceID = &entry.WorkspaceID
	}
	var actorID *int64
	if entry.ActorID > 0 {
		actorID = &entry.ActorID
	}

	// actor 列（TEXT）保留为「user:<id>」：既有读模型与既有 UI 依赖它，
	// 而 actor_user_id 提供可联表的结构化身份。两者写同一次操作，不各写一份。
	actor := "system"
	if entry.ActorID > 0 {
		actor = "user:" + strconv.FormatInt(entry.ActorID, 10)
	}

	_, err := execer.Exec(ctx, `
    INSERT INTO audit_logs
      (actor, action, resource_type, resource_id, detail,
       workspace_id, project_id, actor_user_id, revision, reason, request_id)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		actor, entry.Action, entry.Resource, entry.ResourceID, entry.Detail,
		workspaceID, projectID, actorID, revision, entry.Reason, entry.RequestID)
	return err
}

// AuditEntry 是审计的读模型。
type AuditEntry struct {
	ID          int64     `json:"id"`
	Actor       string    `json:"actor"`
	ActorUserID *int64    `json:"actorUserId,omitempty"`
	Action      string    `json:"action"`
	Resource    string    `json:"resourceType"`
	ResourceID  string    `json:"resourceId"`
	Detail      string    `json:"detail"`
	ProjectID   *int64    `json:"projectId,omitempty"`
	Revision    *int64    `json:"revision,omitempty"`
	Reason      string    `json:"reason"`
	RequestID   string    `json:"requestId"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ListProjectAudit 按项目读审计，时间倒序，稳定游标。
//
// 用途有两类：T27 的项目动态，以及「审计是否真的写了」的测试对账。
// 分页用 (created_at, id) keyset，与项目列表同一套约定（契约 §1.5）。
func (s *AuthzStore) ListProjectAudit(ctx context.Context, projectID int64, cursor time.Time, cursorID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var cursorTime *time.Time
	if !cursor.IsZero() {
		truncated := cursor
		cursorTime = &truncated
	}

	rows, err := s.db.Query(ctx, `
    SELECT id, actor, actor_user_id, action, resource_type, resource_id, detail,
           project_id, revision, reason, request_id, created_at
    FROM audit_logs
    WHERE project_id = $1
      AND ($2::timestamptz IS NULL OR (created_at, id) < ($2::timestamptz, $3::bigint))
    ORDER BY created_at DESC, id DESC
    LIMIT $4`, projectID, cursorTime, cursorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(&entry.ID, &entry.Actor, &entry.ActorUserID, &entry.Action,
			&entry.Resource, &entry.ResourceID, &entry.Detail, &entry.ProjectID,
			&entry.Revision, &entry.Reason, &entry.RequestID, &entry.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	return items, rows.Err()
}

// projectForAuditTx 读取审计需要的项目字段（工作区 ID）。
func projectForAuditTx(ctx context.Context, tx pgx.Tx, projectID int64) (model.Project, error) {
	var project model.Project
	err := tx.QueryRow(ctx, `SELECT id, workspace_id FROM projects WHERE id = $1`, projectID).
		Scan(&project.ID, &project.WorkspaceID)
	return project, err
}

// apiStoreError 是 store 层可直接展示给用户的错误。
//
// 与 userFacingStoreError 的区别：这里不需要绑定底层 cause（没有需要保留身份的
// 驱动错误），只是「用户输入不合法」。handler 会把它映射成 422。
type apiStoreError struct {
	Message string
}

func (e *apiStoreError) Error() string { return e.Message }

// NewStoreValidationError 供包外构造同类型错误。
func NewStoreValidationError(message string) error { return &apiStoreError{Message: message} }

// IsStoreValidationError 判断错误是否为可展示的输入校验错误。
func IsStoreValidationError(err error) bool {
	var target *apiStoreError
	return errors.As(err, &target)
}

// DenialKind 是拒绝的类型。store 层不引用 net/http：
// 把「非成员」与「角色不足」映射成 404 还是 403 是**HTTP 契约**的决定，
// 属于 handler。store 只负责区分这两件不同的事 —— 压成 bool 就会让
// 调用点只能选一个状态码，而选错就是信息泄漏（对非成员返回 403）或误导用户。
type DenialKind int

const (
	// DenialAllowed 表示允许（RequireProjectAccess 在此情况下 ok=true）。
	DenialAllowed DenialKind = iota
	// DenialHidden 表示应返回**资源隐藏型 404**：请求者不是成员，
	// 不得确认项目是否存在。
	DenialHidden
	// DenialForbidden 表示应返回 403：请求者是成员，但角色不够。
	DenialForbidden
)

// RequireProjectAccess 是 handler 层最常用的入口：授权不通过时直接给出
// 拒绝类型与可展示文案，handler 只做「类型 → 状态码」的映射。
//
// 为什么不直接返回状态码：store 层引 net/http 会让「授权规则」与「HTTP 契约」
// 耦合在一起，而契约 §1.2 对两者有不同要求（403 与资源隐藏型 404 的语义不同）。
func (s *AuthzStore) RequireProjectAccess(ctx context.Context, projectID, userID int64, action AuthzAction) (AuthzDecision, DenialKind, string, error) {
	decision, err := s.AuthorizeProject(ctx, projectID, userID, action)
	if err != nil {
		return AuthzDecision{}, DenialForbidden, "", err
	}
	if decision.Allowed {
		return decision, DenialAllowed, "", nil
	}
	if !decision.IsMember {
		return decision, DenialHidden, decision.Reason(action), nil
	}
	return decision, DenialForbidden, decision.Reason(action), nil
}

// NormalizeReason 归一化审计理由：去空白并限长。
//
// 限长的理由：reason 会写入审计表与界面，而它来自请求体。
// 不限制会允许一次请求写入任意大小的一行（也容易被用来撑大日志表）。
func NormalizeReason(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if len([]rune(trimmed)) > 500 {
		trimmed = string([]rune(trimmed)[:500])
	}
	return trimmed
}
