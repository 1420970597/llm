package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件实现设置页需要的读模型与成员命令（Issue #160 T28）。
//
// 契约：#160 T28 的原文要求「连接页复用 provider/storage 管理能力，测试与保存分离，
// 密钥仅写不回显」「团队页实现真实成员添加/移除/角色变更，首版可选择已有账号」
// 「预算显示配置/预留/实际/未知」「普通用户看到适合其权限的配置选择，
// 不拿管理员密钥接口当公共选项列表」。
//
// 三条与权限直接相关的取舍：
//
//  1. **连接选项是独立的只读端点**，不是把 `/api/v1/admin/providers` 开放给所有人。
//     管理员接口返回的是"可写"资源（含 provider 的全部配置），把它当公共选项列表
//     会让任何登录用户拿到治理面的读写入口（T28 验收项明确禁止）。
//  2. **工作区成员管理要求工作区管理员**（AuthzManageMembers）；项目成员管理
//     仍由既有项目端点负责（owner 可管），两者**职责分开**（T28 验收项）。
//  3. **预算对项目成员可读**：项目成员本来就能看到批次的用量与预留；
//     把预算藏起来只会让「为什么跑不动」变成猜谜（429 的提示会指向这里）。

func init() {
	RegisterRoutes(registerStudioSettingsRoutes)
}

func registerStudioSettingsRoutes(mux *http.ServeMux, app *application) {
	mux.HandleFunc("GET /api/v1/workspace/members", app.listWorkspaceMembers)
	mux.HandleFunc("POST /api/v1/workspace/members", app.upsertWorkspaceMember)
	mux.HandleFunc("DELETE /api/v1/workspace/members/{userId}", app.removeWorkspaceMember)
	// issue #197 第 15 条：本部署没有出站邮件，「邮件邀请」这条路永远走不通。
	// 因此新增账号改为「管理员直接设初始密码」：一次命令同时创建账号与成员关系。
	mux.HandleFunc("POST /api/v1/workspace/members/direct", app.createWorkspaceMemberDirect)
	mux.HandleFunc("GET /api/v1/settings/connection-options", app.listConnectionOptions)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/budget", app.getProjectBudget)
}

// ---------------------------------------------------------------------------
// 工作区成员
// ---------------------------------------------------------------------------

type workspaceMemberRequest struct {
	// UserID 与 Email 二选一：界面按邮箱选人（「首版可选择已有账号」），
	// 而脚本/CLI 更习惯用 ID。
	UserID int64  `json:"userId"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	// WorkspaceID 缺省用默认工作区。
	WorkspaceID int64 `json:"workspaceId"`
}

// listWorkspaceMembers 列出工作区成员（任何成员可读）。
func (app *application) listWorkspaceMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := app.authorizeWorkspaceRequest(w, r, store.AuthzRead)
	if !ok {
		return
	}
	members, err := store.NewWorkspaceMemberStore(app.studio.Pool).ListWorkspaceMembers(r.Context(), workspaceID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": members, "nextCursor": "", "sortKey": "role:desc,userId:asc",
		"notes": []string{
			"工作区管理员管理成员与连接；项目内容权限由项目成员决定（两者职责分开）",
			"移除工作区成员会同时清理他在本工作区的项目成员关系：若他仍是某项目的最后一名负责人，移除会被拒绝",
		},
	})
}

// upsertWorkspaceMember 添加成员或变更角色（需要工作区管理员）。
func (app *application) upsertWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	var request workspaceMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}
	queryWorkspaceID, err := workspaceIDFromQuery(r)
	if err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("工作区标识不正确", []model.FieldError{{
			Field: "workspaceId", Message: "必须是正整数",
		}}))
		return
	}
	if queryWorkspaceID > 0 && request.WorkspaceID > 0 && queryWorkspaceID != request.WorkspaceID {
		// 一个命令不能同时声明两个作用域。之前先授权 query、再用 body
		// 覆盖目标，导致 workspace A 的管理员可以改写 workspace B。
		app.writeStudioError(w, r, studio.NewValidationError("请求中的工作区标识不一致，请只保留一个目标工作区", []model.FieldError{{
			Field: "workspaceId", Message: "query 与 body 必须一致",
		}}))
		return
	}
	requestedWorkspaceID := request.WorkspaceID
	if queryWorkspaceID > 0 {
		requestedWorkspaceID = queryWorkspaceID
	}
	workspaceID, ok := app.authorizeWorkspaceForUser(w, r, user.ID, requestedWorkspaceID, store.AuthzManageMembers)
	if !ok {
		return
	}
	members := store.NewWorkspaceMemberStore(app.studio.Pool)
	targetID := request.UserID
	if targetID <= 0 {
		resolved, err := members.ResolveUserIDByEmail(r.Context(), request.Email)
		if err != nil {
			app.writeStudioError(w, r, err)
			return
		}
		targetID = resolved
	}
	member, err := members.UpsertWorkspaceMember(r.Context(), workspaceID, user.ID, targetID, request.Role)
	if err != nil {
		app.writeWorkspaceMemberError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, member)
}

// createWorkspaceMemberRequest 是「直接建号」的请求体（issue #197 第 15 条）。
type createWorkspaceMemberRequest struct {
	// Email 同时是**登录用户名**（本系统的 users.email 就是登录标识）。
	Email string `json:"email"`
	// Password 是管理员设的初始密码。它只在这次请求里出现，绝不回显。
	Password string `json:"password"`
	// Role 是**工作区角色**（admin / member）。
	Role string `json:"role"`
	// UserRole 是该账号的**系统角色**（admin / user）：决定它能否进入兼容控制台。
	UserRole    string `json:"userRole"`
	WorkspaceID int64  `json:"workspaceId"`
}

// CreateWorkspaceMemberDirect 的参数集合（避免 handler 里堆 6 个位置参数）。
type createMemberInput struct {
	WorkspaceID int64
	ActorID     int64
	Email       string
	Password    string
	Role        string
	UserRole    string
}

// toStoreInput 转成 store 层入参。
//
// 两个同名字段集合并存是为了让 handler 只依赖本文件的请求结构，
// 而 store 不反向依赖 API 层的命名；转换点只有这一处，不会漂移。
func (input createMemberInput) toStoreInput() store.CreateWorkspaceMemberWithAccountInput {
	return store.CreateWorkspaceMemberWithAccountInput{
		WorkspaceID: input.WorkspaceID,
		ActorID:     input.ActorID,
		Email:       input.Email,
		Password:    input.Password,
		Role:        input.Role,
		UserRole:    input.UserRole,
	}
}

// createWorkspaceMemberDirect 创建账号并加入工作区（需要工作区管理员）。
//
// 顺序与失败语义（刻意这样设计）：
//  1. 先授权、再校验、最后写库 —— 未授权的请求不能通过「邮箱已存在」这种
//     差异化错误探知系统里有哪些账号；
//  2. 先建账号、再建成员关系。第二步失败只会留下一个**没有工作区**的账号，
//     这是可恢复的（再次提交会走「已存在 → 直接加成员」分支），
//     而反过来会留下「成员指向不存在的用户」的脏关系。
func (app *application) createWorkspaceMemberDirect(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}

	var request createWorkspaceMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}
	workspaceID, ok := app.authorizeWorkspaceForUser(w, r, user.ID, request.WorkspaceID, store.AuthzManageMembers)
	if !ok {
		return
	}

	input := createMemberInput{
		WorkspaceID: workspaceID,
		ActorID:     user.ID,
		Email:       request.Email,
		Password:    request.Password,
		Role:        strings.TrimSpace(request.Role),
		UserRole:    strings.TrimSpace(request.UserRole),
	}
	if input.Role == "" {
		input.Role = model.WorkspaceRoleMember
	}
	if input.UserRole == "" {
		input.UserRole = "user"
	}
	created, member, err := app.studio.CreateWorkspaceMemberWithAccount(r.Context(), input.toStoreInput())
	if err != nil {
		app.writeWorkspaceMemberError(w, r, err)
		return
	}
	// 响应里**不含密码与哈希**：初始密码只在此次请求的请求体里存在。
	app.writeJSON(w, http.StatusCreated, map[string]any{
		"user":   created,
		"member": member,
		// 《首次登录须改密码》目前没有独立的强制机制，因此如实标注为建议。
		"note": "请通过安全渠道把初始密码转交本人，并提示其首次登录后修改密码。",
	})
}

// removeWorkspaceMember 移除成员（需要工作区管理员）。
func (app *application) removeWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := app.authorizeWorkspaceRequest(w, r, store.AuthzManageMembers)
	if !ok {
		return
	}
	user, _ := requestUser(r)
	targetID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("userId")), 10, 64)
	if err != nil || targetID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该成员"))
		return
	}
	if err := store.NewWorkspaceMemberStore(app.studio.Pool).RemoveWorkspaceMember(
		r.Context(), workspaceID, user.ID, targetID); err != nil {
		app.writeWorkspaceMemberError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{"removedUserId": targetID})
}

// writeWorkspaceMemberError 把成员操作的哨兵错误翻译成契约错误。
func (app *application) writeWorkspaceMemberError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrLastWorkspaceAdmin):
		app.writeStudioError(w, r, studio.NewError(studio.CodeConflict, err.Error()))
		return
	case errors.Is(err, store.ErrWorkspaceMemberNotBlocked):
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, err.Error()))
		return
	case errors.Is(err, store.ErrWorkspaceMemberManageDenied):
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden, err.Error()))
		return
	}
	var owns *store.ErrWorkspaceMemberOwnsProjects
	if errors.As(err, &owns) {
		blockers := make([]studio.Blocker, 0, len(owns.Projects))
		for _, project := range owns.Projects {
			// Blocker 带 Link（T22 的约定）：用户要能**直接跳到**那个项目去指定
			// 新的负责人，而不是拿着项目名去列表里找。
			blockers = append(blockers, studio.Blocker{
				Code:    "LAST_PROJECT_OWNER",
				Message: "项目「" + project.Name + "」只剩他一名负责人",
				Link:    "/p/" + strconv.FormatInt(project.ProjectID, 10) + "/overview",
			})
		}
		app.writeStudioError(w, r, studio.NewBlockerError(studio.CodeConflict, owns.Error(), blockers))
		return
	}
	app.writeStudioError(w, r, err)
}

// authorizeWorkspaceRequest 解析工作区并做工作区级授权。
func (app *application) authorizeWorkspaceRequest(w http.ResponseWriter, r *http.Request, action store.AuthzAction) (int64, bool) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return 0, false
	}
	workspaceID, err := workspaceIDFromQuery(r)
	if err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("工作区标识不正确", []model.FieldError{{
			Field: "workspaceId", Message: "必须是正整数",
		}}))
		return 0, false
	}
	return app.authorizeWorkspaceForUser(w, r, user.ID, workspaceID, action)
}

// workspaceIDFromQuery parses the optional workspace scope without silently
// treating malformed IDs as the default workspace.
func workspaceIDFromQuery(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("workspaceId"))
	if raw == "" {
		return 0, nil
	}
	workspaceID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || workspaceID <= 0 {
		return 0, errors.New("invalid workspace id")
	}
	return workspaceID, nil
}

// authorizeWorkspaceForUser authorizes the final scope used by a request.
// Callers must resolve all query/body scope inputs before invoking it; this
// keeps the authorization decision and the write target identical.
func (app *application) authorizeWorkspaceForUser(w http.ResponseWriter, r *http.Request, userID, requestedWorkspaceID int64, action store.AuthzAction) (int64, bool) {
	workspaceID := requestedWorkspaceID
	if workspaceID <= 0 {
		workspace, err := app.projects.DefaultWorkspace(r.Context())
		if err != nil {
			app.writeAPIEntityError(w, r, err)
			return 0, false
		}
		workspaceID = workspace.ID
	}
	decision, err := app.authz.AuthorizeWorkspace(r.Context(), workspaceID, userID, action)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return 0, false
	}
	if !decision.IsMember {
		// 资源隐藏型 404：非成员不该知道这个工作区存在。
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该工作区"))
		return 0, false
	}
	if !decision.Allowed {
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden,
			"需要工作区管理员才能管理成员与连接"))
		return 0, false
	}
	return workspaceID, true
}

// ---------------------------------------------------------------------------
// 连接选项（只读、无密钥）
// ---------------------------------------------------------------------------

// listConnectionOptions 返回可选择的连接与存储选项（**非秘密标识**）。
//
// 为什么单独一个端点而不是开放 admin 接口：见文件头第 1 条。
func (app *application) listConnectionOptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := requestUser(r); !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	providers, err := app.store.ListProviders(r.Context())
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	profiles, err := app.store.ListStorageProfiles(r.Context())
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	type providerOption struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Model        string `json:"model"`
		ProviderType string `json:"providerType"`
		IsActive     bool   `json:"isActive"`
		// APIKeyMasked 是掩码后的标识（**永远不是密钥本体**）。
		APIKeyMasked string `json:"apiKeyMasked"`
	}
	type storageOption struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Provider     string `json:"provider"`
		Endpoint     string `json:"endpoint"`
		Bucket       string `json:"bucket"`
		IsActive     bool   `json:"isActive"`
		IsDefault    bool   `json:"isDefault"`
		SecretMasked string `json:"secretKeyMasked"`
	}
	providerOptions := make([]providerOption, 0, len(providers))
	for _, provider := range providers {
		providerOptions = append(providerOptions, providerOption{
			ID: provider.ID, Name: provider.Name, Model: provider.Model,
			ProviderType: provider.ProviderType, IsActive: provider.IsActive,
			APIKeyMasked: provider.APIKeyMasked,
		})
	}
	storageOptions := make([]storageOption, 0, len(profiles))
	for _, profile := range profiles {
		storageOptions = append(storageOptions, storageOption{
			ID: profile.ID, Name: profile.Name, Provider: profile.Provider,
			Endpoint: profile.Endpoint, Bucket: profile.Bucket,
			IsActive: profile.IsActive, IsDefault: profile.IsDefault,
			SecretMasked: profile.SecretKeyMasked,
		})
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"providers": providerOptions, "storageProfiles": storageOptions,
		"notes": []string{
			"这里只返回非秘密标识：密钥永不回显，也不会因为选择连接而被复制到别处",
			"新增/修改连接与「测试连接」属于管理员设置页（测试与保存分离：测试不会覆盖已保存配置）",
		},
	})
}

// getProjectBudget 返回项目预算台账（成员可读）。
func (app *application) getProjectBudget(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, msgProjectNotFound))
		return
	}
	decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRead)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	currency := decision.Project.Budget.Currency
	snapshot, err := app.studio.Usage.ProjectBudget(r.Context(), projectID, currency)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// 生效上限取「批次上限与项目上限中更严格者」，但项目级展示用项目上限，
	// 并在 notes 里说明未知费用的占用方式（T07 的既定语义）。
	app.writeJSON(w, http.StatusOK, map[string]any{
		"budget": snapshot,
		"notes": []string{
			"未知费用按当时的预留金额占用额度（unknown 不等于 0）：超时/断连时供应商可能已收费",
			"实际（settled）只放精确金额；估计与未知都计入 uncertain，因此「剩余额度」会偏保守",
			model.BudgetOnExhaustedPause + "：额度用尽时阻止新的自动执行，不会静默换更便宜的模型",
		},
	})
}
