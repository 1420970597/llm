package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 Atelier 项目 API（Issue #160 T02/T08）。
//
// 契约：docs/plans/atelier-api-contract.md §1–§3。
//
// 与 `datasets` 的关系：`datasets` 保留为兼容读模型与历史来源（T31 才收口写入口），
// 新运行一律走项目。不把 Project 改名成 Dataset，也不把一个 dataset 的可变配置
// 当成多个批次共享的运行状态。

func init() {
	RegisterRoutes(registerProjectRoutes)
}

// projectPrefix 是项目 API 的固定前缀。
const projectPrefix = "/api/v1/projects"

// registerProjectRoutes 把项目命名空间挂到 ServeMux。
//
// 为什么用 `{projectId}` 通配而不是自己解析路径：Go 1.22+ ServeMux 原生支持
// 通配段，用它比手写 `strings.Split` 更能保证「未知子资源返回 404」这一契约
// （契约 §6「未知子资源返回 404」）—— ServeMux 对未注册 path 直接 404。
func registerProjectRoutes(mux *http.ServeMux, app *application) {
	mux.HandleFunc("GET "+projectPrefix, app.listProjects)
	mux.HandleFunc("POST "+projectPrefix, app.createProject)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}", app.getProject)

	// 成员读模型在 T02 只做读取，增删改角色属于 T28 的成员管理命令。
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/members", app.listProjectMembers)
}

// ---------------------------------------------------------------------------
// 通用响应信封与错误契约（契约 §1.1–§1.4）
// ---------------------------------------------------------------------------

// apiLinks 是对象响应的可跳转链接。前端不自行拼 URL（契约 §1.1）。
type apiLinks map[string]string

// apiEnvelope 是所有单对象响应的稳定外壳。
//
// 字段名与契约 §1.1 一致：id/status/revision/updatedAt/capabilities/links/warnings。
// 具体对象放在 data 里，避免把对象的每个字段都提升到顶层而在不同资源间互相污染。
type apiEnvelope struct {
	ID           string             `json:"id"`
	Status       string             `json:"status"`
	Revision     int64              `json:"revision"`
	UpdatedAt    string             `json:"updatedAt"`
	Capabilities model.Capabilities `json:"capabilities"`
	Links        apiLinks           `json:"links"`
	Warnings     []string           `json:"warnings"`
	Data         any                `json:"data"`
}

// projectResourceID 是项目对象的稳定 ID 形态。
//
// 契约 §2.1 说「201 + 新 project ID」。这里用 `p_<n>` 前缀：纯数字 ID 会让人
// 与 dataset/batch/experiment 的 ID 混淆，而 URL 里 `/p/12/...` 与
// `/p/p_12/...` 的区别正好让「拿 dataset id 拼项目 URL」这类错误立刻可见。
// 解析时同时接受 `p_12` 与 `12`，保证手工调试与既有脚本可用。
func projectResourceID(id int64) string { return "p_" + strconv.FormatInt(id, 10) }

// parseProjectID 从路径参数解析项目 ID。
func parseProjectID(raw string) (int64, error) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "p_")
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, newUserFacingError("项目地址不正确，请从项目列表重新进入", err)
	}
	return id, nil
}

// apiErrorBody 是契约 §1.2 的错误响应。
//
// 注意：它**不**复用 writeError 的 `{"error": "..."}` 形状，因为新契约要求
// code/fieldErrors/blockers/requestId/retryable。两者并存是过渡期的必然结果：
// 旧端点的前端调用方已经依赖 `error` 字符串，改形状会同时打断旧页面。
type apiErrorBody struct {
	Code        string             `json:"code"`
	Message     string             `json:"message"`
	FieldErrors []model.FieldError `json:"fieldErrors,omitempty"`
	Blockers    []apiBlocker       `json:"blockers,omitempty"`
	RequestID   string             `json:"requestId"`
	Retryable   bool               `json:"retryable"`
}

// apiBlocker 是可跳转的阻塞项（契约 §2.8）。
type apiBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Link    string `json:"link,omitempty"`
}

// 错误码常量。前端按 code 分支，不解析中文文案（文案会改，code 不会）。
const (
	codeValidation    = "VALIDATION_FAILED"
	codeUnauthorized  = "UNAUTHORIZED"
	codeForbidden     = "FORBIDDEN"
	codeNotFound      = "NOT_FOUND"
	codeConflict      = "CONFLICT"
	codeRevisionStale = "REVISION_CONFLICT"
	codeIdempotency   = "IDEMPOTENCY_KEY_REUSED"
	codeUnavailable   = "DEPENDENCY_UNAVAILABLE"
)

// writeAPIError 是项目 API 的**唯一**错误出口。
func (app *application) writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string, fieldErrors []model.FieldError) {
	app.writeJSON(w, status, apiErrorBody{
		Code:        code,
		Message:     message,
		FieldErrors: fieldErrors,
		RequestID:   requestID(r),
		Retryable:   status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable,
	})
}

// writeAPIEntityError 把 store/service 层的错误翻译成契约错误码。
//
// 集中在一处而不是每个 handler 自己映射：错误码与状态码的对应关系是**契约**，
// 散落到 20 个 handler 里必然漂移（这正是 writeError 的前身踩过的坑）。
func (app *application) writeAPIEntityError(w http.ResponseWriter, r *http.Request, err error) {
	// 字段级校验：422 + fieldErrors（契约 §2.1）。
	if fieldErrors, ok := model.HasFieldErrors(err); ok {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, err.Error(), fieldErrors)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		app.writeAPIError(w, r, http.StatusNotFound, codeNotFound, msgProjectNotFound, nil)
		return
	}
	if store.IsUniqueViolation(err) {
		app.writeAPIError(w, r, http.StatusConflict, codeConflict,
			"同一工作区内已有同名项目，请换一个名称后重试", nil)
		return
	}
	var apiErr *apiCommandError
	if errors.As(err, &apiErr) {
		app.writeAPIError(w, r, apiErr.Status, apiErr.Code, apiErr.Message, apiErr.FieldErrors)
		return
	}
	// 兜底：保持中文、隐藏内部细节（沿用 writeError 的既有脱敏约定）。
	app.writeError(w, http.StatusInternalServerError, err)
}

// apiCommandError 是带契约错误码的服务层错误。
type apiCommandError struct {
	Status      int
	Code        string
	Message     string
	FieldErrors []model.FieldError
}

func (e *apiCommandError) Error() string { return e.Message }

// requestID 返回本次请求的关联 ID。
//
// 契约 §1.2 要求错误带 requestId，日志也用它串联。中间件目前没有生成 ID，
// 因此这里按「有则复用、无则现算」处理：
//   - 复用请求头让前端/网关能自己种一个 ID，使「用户报错截图」与日志能对上；
//   - 现算时用随机值而不是路径/时间哈希 —— 时间戳在同一毫秒内的两个请求会相同，
//     而排障恰恰需要区分它们。
func requestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" && len(id) <= 128 {
		return id
	}
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(buf[:])
}

// ---------------------------------------------------------------------------
// POST /api/v1/projects
// ---------------------------------------------------------------------------

// createProject 创建项目草稿（契约 §2.1）。
//
// 三条必须成立的性质：
//  1. **无模型调用**：不查 provider、不查 storage、不入队作业；
//  2. **不因缺模型/存储失败**：这类能力在发起批次前才检查；
//  3. **幂等**：同 Idempotency-Key + 同请求摘要返回原项目（不新建第二个）。
func (app *application) createProject(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeAPIError(w, r, http.StatusUnauthorized, codeUnauthorized, msgAuthRequired, nil)
		return
	}

	var input model.CreateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeAPIError(w, r, http.StatusBadRequest, codeValidation,
			"请求格式有误，请检查填写的内容后重试", nil)
		return
	}
	input.Normalize()
	if err := input.Validate(); err != nil {
		fieldErrors, _ := model.HasFieldErrors(err)
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, err.Error(), fieldErrors)
		return
	}

	workspaceID := input.WorkspaceID
	if workspaceID <= 0 {
		workspace, err := app.projects.DefaultWorkspace(r.Context())
		if err != nil {
			app.writeAPIEntityError(w, r, err)
			return
		}
		workspaceID = workspace.ID
	} else {
		// 显式指定工作区时必须存在，否则会写出一个引用不存在工作区的项目。
		if _, err := app.workspaceByID(r.Context(), workspaceID); err != nil {
			app.writeAPIEntityError(w, r, err)
			return
		}
	}

	// 幂等处理：先查记录，命中则回放原结果。
	digest := digestCreateProject(input)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key != "" {
		record, found, err := app.idempotency.Lookup(r.Context(), "projects.create", user.ID, key)
		if err != nil {
			app.writeAPIEntityError(w, r, err)
			return
		}
		if found {
			if record.RequestDigest != digest {
				app.writeAPIError(w, r, http.StatusConflict, codeIdempotency,
					"该请求标识已用于另一次不同的创建请求，请勿复用同一个 Idempotency-Key", nil)
				return
			}
			existing, err := app.projects.GetProject(r.Context(), record.ResourceID)
			if err != nil {
				app.writeAPIEntityError(w, r, err)
				return
			}
			app.writeJSON(w, http.StatusCreated, app.projectEnvelope(existing, model.ProjectRoleOwner))
			return
		}
	}

	project, err := app.projects.CreateProject(r.Context(), workspaceID, user.ID, input)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}

	if key != "" {
		if err := app.idempotency.Save(r.Context(), "projects.create", user.ID, key, digest, project.ID, http.StatusCreated); err != nil {
			// 幂等记录写失败不改变「项目已创建」这一事实，但必须让调用方知道
			// 重试可能创建第二个项目 —— 静默继续会让重复创建无法排查。
			app.logInternal(r, "idempotency save failed for projects.create", err)
		}
	}

	app.writeJSON(w, http.StatusCreated, app.projectEnvelope(project, model.ProjectRoleOwner))
}

// digestCreateProject 计算请求摘要。
//
// 为什么摘要只看**语义字段**而不是整个原始 body：JSON 字段顺序、空白与
// 未提供字段的显式 null 都会改变字节而语义不变。这里对归一化后的结构做哈希，
// 让「同键同请求」的判定符合用户直觉。
func digestCreateProject(input model.CreateProjectInput) string {
	domains, directionsPerDomain, questionsPerDirection := input.CoverageValues()
	normalized := struct {
		Name                  string
		Goal                  string
		TargetKind            string
		WorkspaceID           int64
		Domains               int
		DirectionsPerDomain   int
		QuestionsPerDirection int
		PilotSize             int
		AcceptanceTarget      float64
		Currency              string
		LimitMinor            int64
		OnExhausted           string
	}{
		Name:                  input.Name,
		Goal:                  input.Goal,
		TargetKind:            input.TargetKind,
		WorkspaceID:           input.WorkspaceID,
		Domains:               domains,
		DirectionsPerDomain:   directionsPerDomain,
		QuestionsPerDirection: questionsPerDirection,
		PilotSize:             input.PilotSize,
		AcceptanceTarget:      input.AcceptanceTargetValue(),
		Currency:              input.Budget.Currency,
		LimitMinor:            input.BudgetLimitValue(),
		OnExhausted:           input.Budget.OnExhausted,
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// GET /api/v1/projects
// ---------------------------------------------------------------------------

// listProjects 返回当前用户可见的项目页（契约 §3）。
//
// 可见范围来自 project_members，**不**来自 users.role：workspace admin
// 不默认拥有所有项目内容读权（契约 §1.6）。没有成员关系的项目对该用户
// 完全不可见 —— 不是「可见但 403」，因为那会泄漏项目是否存在。
func (app *application) listProjects(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeAPIError(w, r, http.StatusUnauthorized, codeUnauthorized, msgAuthRequired, nil)
		return
	}

	projectIDs, err := app.projects.ProjectIDsForUser(r.Context(), user.ID)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}
	if len(projectIDs) == 0 {
		// 空列表是合法结果，不是 404：新用户看到「还没有项目」比看到错误好。
		app.writeJSON(w, http.StatusOK, store.ProjectPage{
			Items: []model.Project{}, NextCursor: "", SortKey: "updatedAt:desc",
		})
		return
	}

	cursor, err := store.DecodeProjectCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		app.writeAPIError(w, r, http.StatusBadRequest, codeValidation, err.Error(), nil)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	page, err := app.projects.ListProjects(r.Context(), store.ProjectListQuery{
		ProjectIDs: projectIDs,
		Query:      r.URL.Query().Get("q"),
		Cursor:     cursor,
		Limit:      limit,
	})
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}

	// 列表项带上能力位：卡片上的动作按钮必须与服务端判定一致。
	// 可见集合已由 ProjectIDsForUser 确定，因此这里查不到角色只可能是
	// 「刚刚被移除成员」的并发情况 —— 那种情况下返回空能力（只读壳）是对的。
	items := make([]apiEnvelope, 0, len(page.Items))
	for _, project := range page.Items {
		role, _, err := app.projects.ProjectRole(r.Context(), project.ID, user.ID)
		if err != nil {
			app.writeAPIEntityError(w, r, err)
			return
		}
		items = append(items, app.projectEnvelope(project, role))
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"nextCursor": page.NextCursor,
		"sortKey":    page.SortKey,
	})
}

// ---------------------------------------------------------------------------
// GET /api/v1/projects/{projectId}
// ---------------------------------------------------------------------------

// getProject 返回单个项目。
//
// 非成员一律 404（**资源隐藏型 404**，契约 §1.2）：返回 403 会让「这个项目
// 存在」本身成为可探测信息，而项目名往往就是业务信息。
func (app *application) getProject(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeAPIError(w, r, http.StatusUnauthorized, codeUnauthorized, msgAuthRequired, nil)
		return
	}

	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}
	project, err := app.projects.GetProject(r.Context(), projectID)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}
	role, isMember, err := app.projects.ProjectRole(r.Context(), projectID, user.ID)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}
	if !isMember {
		app.writeAPIError(w, r, http.StatusNotFound, codeNotFound, msgProjectNotFound, nil)
		return
	}

	app.writeJSON(w, http.StatusOK, app.projectEnvelope(project, role))
}

// listProjectMembers 列出项目成员。
func (app *application) listProjectMembers(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeAPIError(w, r, http.StatusUnauthorized, codeUnauthorized, msgAuthRequired, nil)
		return
	}
	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}
	role, isMember, err := app.projects.ProjectRole(r.Context(), projectID, user.ID)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}
	if !isMember {
		app.writeAPIError(w, r, http.StatusNotFound, codeNotFound, msgProjectNotFound, nil)
		return
	}
	// 只有 owner 能看成员名单：成员名单含他人邮箱，属于项目内容之外的治理信息，
	// viewer/reviewer 没有需要的场景。
	if role != model.ProjectRoleOwner {
		app.writeAPIError(w, r, http.StatusForbidden, codeForbidden,
			"只有项目负责人可以查看成员名单", nil)
		return
	}

	members, err := app.projects.ListProjectMembers(r.Context(), projectID)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{"items": members})
}

// ---------------------------------------------------------------------------
// 响应组装
// ---------------------------------------------------------------------------

// projectEnvelope 把项目包装成契约 §1.1 的稳定外壳。
func (app *application) projectEnvelope(project model.Project, role string) apiEnvelope {
	return apiEnvelope{
		ID:           projectResourceID(project.ID),
		Status:       project.Status,
		Revision:     project.RowVersion,
		UpdatedAt:    project.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		Capabilities: model.ProjectCapabilities(role, project.Status),
		Links: apiLinks{
			"self":      projectPrefix + "/" + projectResourceID(project.ID),
			"overview":  "/p/" + projectResourceID(project.ID) + "/overview",
			"blueprint": "/p/" + projectResourceID(project.ID) + "/blueprint",
			"members":   projectPrefix + "/" + projectResourceID(project.ID) + "/members",
		},
		Warnings: []string{},
		Data:     project,
	}
}

// workspaceByID 读取指定工作区（存在性校验用）。
func (app *application) workspaceByID(ctx context.Context, workspaceID int64) (model.Workspace, error) {
	return app.projects.GetWorkspace(ctx, workspaceID)
}

// bootstrapUserID 返回默认工作区的初始 admin。
//
// 为什么用配置文件里的管理员邮箱反查用户，而不是写死 ID：引导顺序里
// `EnsureBootstrapUser` 已保证该用户存在，而 ID 在不同环境不同。
// 查不到时返回 0，`EnsureDefaultWorkspace` 会跳过成员行 —— 那不阻塞启动，
// 只是需要管理员稍后把自己加进工作区（T28 的成员管理）。
func bootstrapUserID(ctx context.Context, app *application) int64 {
	user, err := app.auth.GetUserByEmail(ctx, app.cfg.DefaultAdminEmail)
	if err != nil {
		log.Printf("WARNING: 无法解析默认工作区的初始管理员（%s）: %v", app.cfg.DefaultAdminEmail, err)
		return 0
	}
	return user.ID
}

// logInternal 记录内部错误但不泄漏给客户端。
//
// 带 requestId 与 path：契约 §1.2 要求「日志带 requestId 但不含密钥」，
// 而只打错误本身会让「用户报的这个错」无法定位到具体请求。
func (app *application) logInternal(r *http.Request, message string, err error) {
	log.Printf("internal error request_id=%s method=%s path=%s message=%s err=%v",
		requestID(r), r.Method, r.URL.Path, message, err)
}

// capabilitiesFor 按当前用户与项目角色算能力位；非成员返回空能力。
func (app *application) capabilitiesFor(ctx context.Context, projectID, userID int64, status string) model.Capabilities {
	role, isMember, err := app.projects.ProjectRole(ctx, projectID, userID)
	if err != nil || !isMember {
		return model.Capabilities{}
	}
	return model.ProjectCapabilities(role, status)
}

const msgProjectNotFound = "未找到该项目，请返回项目列表确认它是否已被删除或你没有访问权限"
