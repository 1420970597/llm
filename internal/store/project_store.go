package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现 Atelier 项目与工作区作用域（Issue #160 T02）。
//
// 契约：docs/plans/atelier-implementation.md §4.1、docs/plans/atelier-api-contract.md §2.1。
//
// 关键设计不变量：
//  1. **创建项目草稿不依赖 provider/storage，也不调用模型**（#160 §1）；
//     执行能力在发起批次前才检查（T07/T13）。因此本文件不出现任何 provider 查询。
//  2. **失败事务不留半个项目**：项目行与 owner 成员行在同一事务内写入，
//     否则会出现「有项目但没人有权限」的孤儿。
//  3. **默认工作区幂等**：用 slug 唯一键 upsert，不靠「第一条记录」。

// DefaultWorkspaceSlug 是首版唯一工作区的固定 slug（契约 §4.1「首版可以默认一个 workspace」）。
const DefaultWorkspaceSlug = "default"

// userFacingStoreError 携带一段面向用户的中文文案，同时让 errors.Is 继续可用。
//
// 为什么 store 层需要它：默认工作区未初始化是**运维/迁移**问题，不是用户输入问题，
// 但用户看到的文案必须能指导下一步（重启重试 / 找管理员），而不是
// `no rows in result set`。apps/api 的 writeError 会优先透出这一段文案。
type userFacingStoreError struct {
	msg   string
	cause error
}

func (e userFacingStoreError) Error() string { return e.msg }

func (e userFacingStoreError) Unwrap() error { return e.cause }

func newUserFacingStoreError(msg string) error {
	return userFacingStoreError{msg: msg, cause: pgx.ErrNoRows}
}

// DefaultWorkspaceName 是默认工作区的展示名。
const DefaultWorkspaceName = "默认工作区"

// 分页游标契约（契约 §1.5）：排序键必须稳定且唯一，末位追加 id。
const (
	projectSortKey = "updatedAt:desc"
	// projectPageLimit 是单页上限。客户端传更大值会被夹到该上限，
	// 而不是报错 —— 报错会让「复制上一个链接」这种常见操作失败。
	projectPageLimit    = 50
	projectDefaultLimit = 20
)

type ProjectStore struct {
	db *pgxpool.Pool
}

func NewProjectStore(db *pgxpool.Pool) *ProjectStore {
	return &ProjectStore{db: db}
}

// EnsureDefaultWorkspace 幂等创建默认工作区，并把指定用户加为该工作区的 admin。
//
// 为什么 admin 关系也要幂等写入：初始化发生在每次启动，而「工作区存在但没有任何
// admin」会让成员管理永久不可用（T28 依赖它）。这里用 ON CONFLICT DO NOTHING，
// 既不覆盖已调整的角色，也不留下无 admin 的空工作区。
func (s *ProjectStore) EnsureDefaultWorkspace(ctx context.Context, bootstrapUserID int64) (model.Workspace, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Workspace{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var workspace model.Workspace
	var createdBy *int64
	if bootstrapUserID > 0 {
		createdBy = &bootstrapUserID
	}
	err = tx.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug, created_by)
    VALUES ($1, $2, $3)
    ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
    RETURNING id, name, slug, created_by, created_at, updated_at`,
		DefaultWorkspaceName, DefaultWorkspaceSlug, createdBy,
	).Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.CreatedBy,
		&workspace.CreatedAt, &workspace.UpdatedAt)
	if err != nil {
		return model.Workspace{}, fmt.Errorf("ensure default workspace: %w", err)
	}

	if bootstrapUserID > 0 {
		if _, err := tx.Exec(ctx, `
      INSERT INTO workspace_members (workspace_id, user_id, role, created_by)
      VALUES ($1, $2, 'admin', $2)
      ON CONFLICT (workspace_id, user_id) DO NOTHING`,
			workspace.ID, bootstrapUserID); err != nil {
			return model.Workspace{}, fmt.Errorf("ensure default workspace admin: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Workspace{}, err
	}
	return workspace, nil
}

// DefaultWorkspace 读取默认工作区；不存在返回 pgx.ErrNoRows。
func (s *ProjectStore) DefaultWorkspace(ctx context.Context) (model.Workspace, error) {
	var workspace model.Workspace
	err := s.db.QueryRow(ctx, `
    SELECT id, name, slug, created_by, created_at, updated_at
    FROM workspaces WHERE slug = $1`, DefaultWorkspaceSlug,
	).Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.CreatedBy,
		&workspace.CreatedAt, &workspace.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// 把「默认工作区还不存在」翻译成可操作提示：正常启动路径会幂等创建它，
		// 走到这里说明迁移/引导没跑完，用户能做的事是重启并检查迁移日志。
		return model.Workspace{}, newUserFacingStoreError(
			"工作区尚未初始化完成，请稍后重试；若持续失败请联系管理员检查数据库迁移")
	}
	return workspace, err
}

// GetWorkspace 按 ID 读取工作区；不存在返回 pgx.ErrNoRows。
func (s *ProjectStore) GetWorkspace(ctx context.Context, workspaceID int64) (model.Workspace, error) {
	var workspace model.Workspace
	err := s.db.QueryRow(ctx, `
    SELECT id, name, slug, created_by, created_at, updated_at
    FROM workspaces WHERE id = $1`, workspaceID,
	).Scan(&workspace.ID, &workspace.Name, &workspace.Slug, &workspace.CreatedBy,
		&workspace.CreatedAt, &workspace.UpdatedAt)
	return workspace, err
}

// WorkspaceRole 返回用户在工作区中的治理角色。
//
// 第二个返回值为 false 表示「不是成员」。调用方必须把这个结果当作
// **唯一**的治理权限依据：登录 cookie 里的 users.role 只是历史角色，
// 成员变更后它不会立即变化（T03 验收项要求撤权即时生效）。
func (s *ProjectStore) WorkspaceRole(ctx context.Context, workspaceID, userID int64) (string, bool, error) {
	if userID <= 0 {
		return "", false, nil
	}
	var role string
	err := s.db.QueryRow(ctx, `
    SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return role, true, nil
}

// NormalizeCreateProjectInput 归一化外部传入的创建请求（填默认值）。
//
// 为什么在 store 层再暴露一次：写入口有三个（HTTP handler、T26 的方案复制、
// T30 迁移），而「默认值」只应该有一份定义。handler 与 store 都调它，
// 重复调用是幂等的；漏调一处就会写出 CHECK 约束不接受的值（实测会得到
// SQLSTATE 23514 而不是可读的字段错误）。
func NormalizeCreateProjectInput(input model.CreateProjectInput) model.CreateProjectInput {
	input.Normalize()
	return input
}

// CreateProject 在**单个事务**内创建项目草稿、owner 成员行与创建审计。
//
// 不查 provider/storage，也不入队任何作业：创建草稿是纯设计动作（§2.1）。
// row_version 起始为 1，后续每次写操作 +1，供乐观锁重试。
//
// 入口自己先 Normalize + Validate：数据库 CHECK 约束会拦住非法值，
// 但它只给出 SQLSTATE 与列名，用户看不出该怎么改；而 store 还是
// 非 HTTP 调用方（方案复制、迁移）的入口，那里没有 handler 层校验可依赖。
// 校验先于事务，因此非法输入不会留下半写入。
func (s *ProjectStore) CreateProject(ctx context.Context, workspaceID, actorID int64, input model.CreateProjectInput) (model.Project, error) {
	input = NormalizeCreateProjectInput(input)
	if err := input.Validate(); err != nil {
		return model.Project{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Project{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	project, err := createProjectTx(ctx, tx, workspaceID, actorID, input)
	if err != nil {
		return model.Project{}, err
	}

	if err := insertProjectOwnerTx(ctx, tx, project.ID, actorID); err != nil {
		return model.Project{}, err
	}

	if err := writeProjectAuditTx(ctx, tx, actorID, "create", "project", project.ID, project.Name); err != nil {
		return model.Project{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.Project{}, err
	}
	return project, nil
}

// createProjectTx 是项目行的插入，抽出来以便 T26 的「按方案复制项目」在同一事务里复用。
func createProjectTx(ctx context.Context, tx pgx.Tx, workspaceID, actorID int64, input model.CreateProjectInput) (model.Project, error) {
	var project model.Project
	var legacyDatasetID *int64
	domains, directionsPerDomain, questionsPerDirection := input.CoverageValues()
	// 项目内唯一名冲突会返回 23505；上层把它翻译成 409（不是 500）。
	err := tx.QueryRow(ctx, `
    INSERT INTO projects (
      workspace_id, name, goal, target_kind, status, owner_id,
      domain_count, directions_per_domain, questions_per_direction, pilot_size,
      acceptance_rate_target, budget_currency, budget_limit_minor, budget_on_exhausted,
      legacy_dataset_id, created_by)
    VALUES ($1, $2, $3, $4, 'draft', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
    RETURNING id, workspace_id, name, goal, target_kind, status, owner_id,
              domain_count, directions_per_domain, questions_per_direction, pilot_size,
              acceptance_rate_target::float8, budget_currency, budget_limit_minor,
              budget_on_exhausted, row_version, legacy_dataset_id, created_at, updated_at`,
		workspaceID, input.Name, input.Goal, input.TargetKind, actorID,
		domains, directionsPerDomain, questionsPerDirection,
		input.PilotSize, input.AcceptanceTargetValue(), input.Budget.Currency,
		input.BudgetLimitValue(), input.Budget.OnExhausted, legacyDatasetID, actorID,
	).Scan(&project.ID, &project.WorkspaceID, &project.Name, &project.Goal, &project.TargetKind,
		&project.Status, &project.OwnerID, &project.DomainCount, &project.DirectionsPerDomain,
		&project.QuestionsPerDirection, &project.PilotSize, &project.AcceptanceRateTarget,
		&project.Budget.Currency, &project.Budget.LimitMinor, &project.Budget.OnExhausted,
		&project.RowVersion, &project.LegacyDatasetID, &project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		return model.Project{}, err
	}
	return project, nil
}

// insertProjectOwnerTx 在同一事务里把创建者写成项目 owner。
//
// 为什么必须同事务：项目行提交了但成员行没提交，会得到一个
// 「谁都进不去、也没人能加成员」的项目 —— 那比创建失败更难恢复。
func insertProjectOwnerTx(ctx context.Context, tx pgx.Tx, projectID, ownerID int64) error {
	_, err := tx.Exec(ctx, `
    INSERT INTO project_members (project_id, user_id, role, created_by)
    VALUES ($1, $2, 'owner', $2)
    ON CONFLICT (project_id, user_id) DO UPDATE SET role = 'owner', updated_at = NOW()`,
		projectID, ownerID)
	return err
}

// writeProjectAuditTx 写审计（T03 的完整审计在 0023 迁移，这里先用现有的 audit_logs）。
//
// 同事务写入：审计「写成功但永久无审计」是不可接受的状态（T03 验收项）。
//
// actor 与 resource_id 在 audit_logs 里是 TEXT 列，因此转换在 Go 侧用 strconv
// 完成后作为**参数值**绑定（不是拼进 SQL 文本）—— 语句结构完全静态，
// 且显式转换让 `'user:' || $1::bigint` 那种让 Postgres 反推参数类型为 text 的
// 写法不再必要。
func writeProjectAuditTx(ctx context.Context, tx pgx.Tx, actorID int64, action, resourceType string, resourceID int64, detail string) error {
	_, err := tx.Exec(ctx, `
    INSERT INTO audit_logs (actor, action, resource_type, resource_id, detail)
    VALUES ($1, $2, $3, $4, $5)`,
		"user:"+strconv.FormatInt(actorID, 10), action, resourceType,
		strconv.FormatInt(resourceID, 10), detail)
	return err
}

// ProjectCursor 是项目列表的稳定游标。
//
// 契约 §1.5 要求「翻页无重复、无遗漏」。实现方式是 keyset 分页：
// 排序键 (updated_at DESC, id DESC)，游标携带上一页最后一行的 (updated_at, id)，
// 下一段查询用严格小于比较。**不用 OFFSET** —— 项目列表会被并发修改，
// OFFSET 在第 2 页开始就会漏项或重复项。
type ProjectCursor struct {
	UpdatedAt time.Time `json:"t"`
	ID        int64     `json:"i"`
}

// EncodeProjectCursor 把游标编码成不透明字符串。
//
// 为什么 base64 而不是直接暴露 JSON：游标是**服务端内部**的翻页状态，
// 前端不该依赖它的字段名；显式编码能让「前端自己拼游标」自然失败。
func EncodeProjectCursor(cursor ProjectCursor) string {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// DecodeProjectCursor 解析游标；空串返回零值（表示从头开始）。
func DecodeProjectCursor(value string) (ProjectCursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ProjectCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ProjectCursor{}, fmt.Errorf("分页游标格式不正确，请重新打开列表")
	}
	var cursor ProjectCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return ProjectCursor{}, fmt.Errorf("分页游标格式不正确，请重新打开列表")
	}
	return cursor, nil
}

// ProjectPage 是一页项目列表。
type ProjectPage struct {
	Items      []model.Project `json:"items"`
	NextCursor string          `json:"nextCursor"`
	SortKey    string          `json:"sortKey"`
}

// ProjectListQuery 是列表过滤条件。
//
// ProjectIDs 是**调用方算出的可见项目集**（`ProjectIDsForUser` 的结果）。
// 为什么不用 workspace 而用项目成员关系：契约 §1.6 明确 workspace admin
// 不默认拥有项目内容读权，因此「可见」的粒度必须是项目而不是工作区。
// ProjectIDs 为空表示「无可见项目」，调用方应直接返回空页，**不要**把它
// 当成「不过滤」——那会变成全库读取，这是最容易写出的越权。
//
// 归档项目永远不在列表里：归档只阻止新运行，不删数据，用户仍可直达
// `/p/{id}/overview`（读与下载保持可用）。
//
// Query 只过滤**项目本身**（名称/目标），不改变项目数据（#159 §1）。
type ProjectListQuery struct {
	ProjectIDs []int64
	Query      string
	Cursor     ProjectCursor
	Limit      int
}

// 两条列表语句都是**静态常量**：唯一变化的是参数个数与绑定值，
// 列名、操作符、ORDER BY 与 LIMIT 都不来自请求（契约 §1.5 用稳定排序键）。
// 无游标时传 NULL，用 `$n IS NULL OR ...` 让首页与后续页共用同一语句。
const (
	listProjectsSQL = `
  SELECT id, workspace_id, name, goal, target_kind, status, owner_id,
         domain_count, directions_per_domain, questions_per_direction, pilot_size,
         acceptance_rate_target::float8, budget_currency, budget_limit_minor,
         budget_on_exhausted, row_version, legacy_dataset_id, created_at, updated_at
  FROM projects
  WHERE id = ANY($1) AND status <> 'archived'
    AND ($2::timestamptz IS NULL OR (updated_at, id) < ($2::timestamptz, $3::bigint))
  ORDER BY updated_at DESC, id DESC
  LIMIT $4`

	listProjectsSearchSQL = `
  SELECT id, workspace_id, name, goal, target_kind, status, owner_id,
         domain_count, directions_per_domain, questions_per_direction, pilot_size,
         acceptance_rate_target::float8, budget_currency, budget_limit_minor,
         budget_on_exhausted, row_version, legacy_dataset_id, created_at, updated_at
  FROM projects
  WHERE id = ANY($1) AND status <> 'archived'
    AND (name ILIKE $2 OR goal ILIKE $2)
    AND ($3::timestamptz IS NULL OR (updated_at, id) < ($3::timestamptz, $4::bigint))
  ORDER BY updated_at DESC, id DESC
  LIMIT $5`
)

// ListProjects 返回一页项目，按 updated_at DESC, id DESC。
//
// 实现取舍（契约 §1.5「翻页无重复、无遗漏」）：
//   - 用 keyset 而非 OFFSET：项目列表会被并发修改，OFFSET 从第 2 页起就会漏项；
//   - 取 limit+1 条判断「还有下一页」，避免额外一次 COUNT；
//   - 排序键 (updated_at, id) 必须全序，否则同一 updated_at 的项目会在页边界重复或丢失。
func (s *ProjectStore) ListProjects(ctx context.Context, query ProjectListQuery) (ProjectPage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = projectDefaultLimit
	}
	if limit > projectPageLimit {
		limit = projectPageLimit
	}
	// 空可见集合不等于「不过滤」：显式返回空页，**不能**继续往下走到不带
	// `id = ANY(...)` 的语句（那会让新用户或撤权用户看到全库项目）。
	if len(query.ProjectIDs) == 0 {
		return ProjectPage{Items: []model.Project{}, SortKey: projectSortKey}, nil
	}

	// 无游标时传 NULL 时间，让 `$n IS NULL OR ...` 恒真。
	var cursorTime *time.Time
	var cursorID int64
	if !query.Cursor.UpdatedAt.IsZero() {
		truncated := query.Cursor.UpdatedAt
		cursorTime = &truncated
		cursorID = query.Cursor.ID
	}

	var rows pgx.Rows
	var err error
	if q := strings.TrimSpace(query.Query); q != "" {
		rows, err = s.db.Query(ctx, listProjectsSearchSQL, query.ProjectIDs,
			"%"+q+"%", cursorTime, cursorID, limit+1)
	} else {
		rows, err = s.db.Query(ctx, listProjectsSQL, query.ProjectIDs, cursorTime, cursorID, limit+1)
	}
	if err != nil {
		return ProjectPage{}, err
	}
	defer rows.Close()

	items := []model.Project{}
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return ProjectPage{}, err
		}
		items = append(items, project)
	}
	if err := rows.Err(); err != nil {
		return ProjectPage{}, err
	}

	page := ProjectPage{Items: items, SortKey: projectSortKey}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = EncodeProjectCursor(ProjectCursor{UpdatedAt: last.UpdatedAt, ID: last.ID})
	}
	return page, nil
}

// GetProject 读取单个项目；不存在返回 pgx.ErrNoRows。
func (s *ProjectStore) GetProject(ctx context.Context, projectID int64) (model.Project, error) {
	row := s.db.QueryRow(ctx, `
    SELECT id, workspace_id, name, goal, target_kind, status, owner_id,
           domain_count, directions_per_domain, questions_per_direction, pilot_size,
           acceptance_rate_target::float8, budget_currency, budget_limit_minor,
           budget_on_exhausted, row_version, legacy_dataset_id, created_at, updated_at
    FROM projects WHERE id = $1`, projectID)
	return scanProject(row)
}

// ProjectRole 返回用户在项目中的内容角色。
//
// 第二个返回值为 false 表示「不是成员」。注意 workspace admin **不**自动返回 owner：
// 契约 §1.6 明确「workspace admin 管理成员/连接，不默认拥有所有项目内容读权」，
// 额外访问需要显式成员授权。
func (s *ProjectStore) ProjectRole(ctx context.Context, projectID, userID int64) (string, bool, error) {
	if userID <= 0 {
		return "", false, nil
	}
	var role string
	err := s.db.QueryRow(ctx, `
    SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return role, true, nil
}

// ListProjectMembers 列出项目成员（含邮箱，便于成员管理页展示）。
func (s *ProjectStore) ListProjectMembers(ctx context.Context, projectID int64) ([]model.ProjectMember, error) {
	rows, err := s.db.Query(ctx, `
    SELECT m.project_id, m.user_id, COALESCE(u.email, ''), m.role, m.created_at, m.updated_at
    FROM project_members m
    LEFT JOIN users u ON u.id = m.user_id
    WHERE m.project_id = $1
    ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'reviewer' THEN 1 ELSE 2 END, m.user_id`,
		projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.ProjectMember{}
	for rows.Next() {
		var item model.ProjectMember
		if err := rows.Scan(&item.ProjectID, &item.UserID, &item.Email, &item.Role,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ProjectIDsForUser 返回用户是成员的全部项目 ID（按项目 ID 升序）。
//
// 这是「今日工作」「命令搜索」等聚合读模型的可见范围来源（T27）。
// 刻意返回 ID 列表而不是直接拼 SQL：调用方必须显式把可见范围带进查询，
// 一旦忘记就会变成全库读取，而那种越权不会在单项目测试里暴露。
func (s *ProjectStore) ProjectIDsForUser(ctx context.Context, userID int64) ([]int64, error) {
	if userID <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
    SELECT project_id FROM project_members WHERE user_id = $1 ORDER BY project_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ProjectCountsByWorkspace 统计工作区下的项目数（含归档），供治理页与测试对账。
func (s *ProjectStore) ProjectCountsByWorkspace(ctx context.Context, workspaceID int64) (total, archived int, err error) {
	err = s.db.QueryRow(ctx, `
    SELECT COUNT(*), COUNT(*) FILTER (WHERE status = 'archived')
    FROM projects WHERE workspace_id = $1`, workspaceID).Scan(&total, &archived)
	return total, archived, err
}

// scanProject 读取一行项目。
//
// 抽出来的原因：GetProject 与 ListProjects 必须用同一列清单与同一顺序扫描，
// 否则两处漂移会在「列表正确但详情错列」时才暴露。
func scanProject(row pgx.Row) (model.Project, error) {
	var project model.Project
	err := row.Scan(&project.ID, &project.WorkspaceID, &project.Name, &project.Goal,
		&project.TargetKind, &project.Status, &project.OwnerID, &project.DomainCount,
		&project.DirectionsPerDomain, &project.QuestionsPerDirection, &project.PilotSize,
		&project.AcceptanceRateTarget, &project.Budget.Currency, &project.Budget.LimitMinor,
		&project.Budget.OnExhausted, &project.RowVersion, &project.LegacyDatasetID,
		&project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		return model.Project{}, err
	}
	return project, nil
}

// IsUniqueViolation 判断错误是否为 Postgres 唯一约束冲突（SQLSTATE 23505）。
//
// 供 handler 把「项目内重名」翻译成 409 而不是 500：用户可以改个名字重试，
// 那不是服务端故障。
func IsUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}
