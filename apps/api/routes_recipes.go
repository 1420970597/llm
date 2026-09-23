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

// 本文件实现方案库的命令与读模型（Issue #160 T26）。
//
// 契约：docs/plans/atelier-api-contract.md §1（信封/错误/幂等）、
// sql/migrations/0032_studio_recipes.sql（表结构取舍）、#160 T26 的原文要求。
//
// 方案是**工作区作用域**对象，因此不复用 `studio.Service`（后者的授权与
// 读模型都以项目为入口）：这里直接调用 `RecipeStore`，并用
// `AuthorizeWorkspace` 做成员校验。
//
// 权限模型（T26「明确版本发布权限」）：
//   - 列表/读取：工作区成员可读 `workspace` 可见度的方案；`private` 只对创建者可见
//     （在 store 层判定，**工作区管理员也不例外** —— T03 明确 admin 不默认拥有
//     所有内容读权）；
//   - 创建：工作区成员；
//   - 保存/发布版本：创建者或工作区管理员（管理员兜底是为了能修复一个坏掉的方案）。

func init() {
	RegisterRoutes(registerRecipeRoutes)
}

func registerRecipeRoutes(mux *http.ServeMux, app *application) {
	mux.HandleFunc("POST /api/v1/recipes", app.createRecipe)
	mux.HandleFunc("GET /api/v1/recipes", app.listRecipes)
	mux.HandleFunc("GET /api/v1/recipes/{recipeId}", app.getRecipe)
	mux.HandleFunc("POST /api/v1/recipes/{recipeId}/versions", app.saveRecipeVersion)
	mux.HandleFunc("POST /api/v1/recipes/{recipeId}/versions/{version}/publish", app.publishRecipeVersion)
}

// recipeCreateRequest 是创建方案的请求体。
type recipeCreateRequest struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	TargetKind      string   `json:"targetKind"`
	Visibility      string   `json:"visibility"`
	ApplicableScope string   `json:"applicableScope"`
	Limitations     []string `json:"limitations"`
	ChangeReason    string   `json:"changeReason"`
	// Publish 为 true 时首个版本直接进入 published（「保存并发布」一步完成）。
	Publish     bool                `json:"publish"`
	WorkspaceID int64               `json:"workspaceId"`
	Payload     model.RecipePayload `json:"payload"`
}

// recipeVersionRequest 是保存方案新版本的请求体。
type recipeVersionRequest struct {
	Payload      model.RecipePayload `json:"payload"`
	ChangeReason string              `json:"changeReason"`
	Publish      bool                `json:"publish"`
}

// createRecipe 创建方案与其首个版本。
func (app *application) createRecipe(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	var request recipeCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}

	workspaceID, ok := app.resolveRecipeWorkspace(w, r, user.ID, request.WorkspaceID)
	if !ok {
		return
	}

	recipe, version, err := app.recipes.CreateRecipe(r.Context(), workspaceID, user.ID, model.CreateRecipeInput{
		Name: request.Name, Description: request.Description, TargetKind: request.TargetKind,
		Visibility: request.Visibility, ApplicableScope: request.ApplicableScope,
		Limitations: request.Limitations, ChangeReason: request.ChangeReason, Payload: request.Payload,
	}, request.Publish)
	if err != nil {
		app.writeRecipeError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusCreated, app.recipeDetailEnvelope(recipe, user.ID, []model.RecipeVersion{version}))
}

// listRecipes 列出用户可见的方案。
func (app *application) listRecipes(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	workspaceID, ok := app.resolveRecipeWorkspace(w, r, user.ID, parseInt64Query(r, "workspaceId"))
	if !ok {
		return
	}
	targetKind := strings.TrimSpace(r.URL.Query().Get("targetKind"))
	if targetKind != "" && targetKind != model.TargetKindSFT && targetKind != model.TargetKindGRPO {
		app.writeStudioError(w, r, studio.NewValidationError("targetKind 只能是 sft 或 grpo",
			[]model.FieldError{{Field: "targetKind", Message: "只能是 sft 或 grpo"}}))
		return
	}
	recipes, err := app.recipes.ListRecipes(r.Context(), workspaceID, user.ID, targetKind, parseIntQuery(r, "limit", 50))
	if err != nil {
		app.writeRecipeError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": recipes, "nextCursor": "", "sortKey": "updatedAt:desc",
	})
}

// getRecipe 返回方案 + 版本列表。
func (app *application) getRecipe(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	recipeID, ok := app.parseRecipeID(w, r, "recipeId")
	if !ok {
		return
	}
	recipe, err := app.recipes.GetRecipe(r.Context(), recipeID, user.ID)
	if err != nil {
		app.writeRecipeError(w, r, err)
		return
	}
	versions, err := app.recipes.ListRecipeVersions(r.Context(), recipeID, 50)
	if err != nil {
		app.writeRecipeError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, app.recipeDetailEnvelope(recipe, user.ID, versions))
}

// saveRecipeVersion 追加一个方案版本。
func (app *application) saveRecipeVersion(w http.ResponseWriter, r *http.Request) {
	user, recipe, ok := app.authorizeRecipeWrite(w, r)
	if !ok {
		return
	}
	var request recipeVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}
	version, err := app.recipes.SaveRecipeVersion(r.Context(), recipe.ID, user.ID, model.SaveRecipeVersionInput{
		Payload: request.Payload, ChangeReason: request.ChangeReason, Publish: request.Publish,
	})
	if err != nil {
		app.writeRecipeError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusCreated, version)
}

// publishRecipeVersion 发布某个版本（T26「明确版本发布权限」）。
func (app *application) publishRecipeVersion(w http.ResponseWriter, r *http.Request) {
	user, recipe, ok := app.authorizeRecipeWrite(w, r)
	if !ok {
		return
	}
	version, err := strconv.Atoi(strings.TrimSpace(r.PathValue("version")))
	if err != nil || version <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该方案版本"))
		return
	}
	published, err := app.recipes.PublishRecipeVersion(r.Context(), recipe.ID, version, user.ID)
	if err != nil {
		app.writeRecipeError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, published)
}

// ---------------------------------------------------------------------------
// 授权与辅助
// ---------------------------------------------------------------------------

// resolveRecipeWorkspace 解析并校验目标工作区（缺省用默认工作区）。
//
// 返回 false 表示已经写了响应。
func (app *application) resolveRecipeWorkspace(w http.ResponseWriter, r *http.Request, userID, requested int64) (int64, bool) {
	return app.resolveWorkspaceForMember(w, r, userID, requested)
}

// authorizeRecipeWrite 校验「可以修改这个方案」并返回方案。
//
// 判据：创建者，或工作区管理员。**私有方案的判定不在这里** ——
// store 的 GetRecipe 已经按可见性过滤，非创建者读不到它，
// 因此这里只需要判「能不能写」。
func (app *application) authorizeRecipeWrite(w http.ResponseWriter, r *http.Request) (model.User, model.Recipe, bool) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return model.User{}, model.Recipe{}, false
	}
	recipeID, ok := app.parseRecipeID(w, r, "recipeId")
	if !ok {
		return model.User{}, model.Recipe{}, false
	}
	recipe, err := app.recipes.GetRecipe(r.Context(), recipeID, user.ID)
	if err != nil {
		app.writeRecipeError(w, r, err)
		return model.User{}, model.Recipe{}, false
	}
	if recipe.CreatedBy != nil && *recipe.CreatedBy == user.ID {
		return user, recipe, true
	}
	role, isMember, err := app.projects.WorkspaceRole(r.Context(), recipe.WorkspaceID, user.ID)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return model.User{}, model.Recipe{}, false
	}
	if !isMember || role != model.WorkspaceRoleAdmin {
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden,
			"只有方案的创建者或工作区管理员可以保存/发布该方案的版本"))
		return model.User{}, model.Recipe{}, false
	}
	return user, recipe, true
}

// recipeDetailEnvelope 组装方案详情的信封。
func (app *application) recipeDetailEnvelope(recipe model.Recipe, userID int64, versions []model.RecipeVersion) map[string]any {
	// Capabilities 只辅助 UI（判定一律在服务端）：创建者能写，其他成员只读。
	canWrite := recipe.CreatedBy != nil && *recipe.CreatedBy == userID
	return map[string]any{
		"id":           strconv.FormatInt(recipe.ID, 10),
		"status":       recipeStatusFor(recipe),
		"revision":     int64(recipe.LatestVersion),
		"updatedAt":    recipe.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"capabilities": map[string]bool{"canEdit": canWrite, "canPublish": canWrite, "canCopy": recipe.PublishedVersion > 0},
		"links": map[string]string{
			"self": "/api/v1/recipes/" + strconv.FormatInt(recipe.ID, 10),
			"page": "/recipes/" + strconv.FormatInt(recipe.ID, 10),
		},
		"warnings": []string{},
		"data":     map[string]any{"recipe": recipe, "versions": versions},
	}
}

// recipeStatusFor 派生展示状态。
//
// 「已发布」表示**至少有一个 published 版本**：方案本身没有工作流状态，
// 用户关心的是「能不能拿去建项目」。没有已发布版本时是 draft（只能自己看/继续改）。
func recipeStatusFor(recipe model.Recipe) string {
	if recipe.PublishedVersion > 0 {
		return "published"
	}
	return "draft"
}

// writeRecipeError 把 store 的哨兵错误翻译成契约错误。
func (app *application) writeRecipeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrRecipeNotFound), errors.Is(err, store.ErrRecipeVersionNotFound):
		// 不可见与不存在用同一个 404：对不可见者确认「它存在」本身就是信息泄露。
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该方案"))
		return
	case errors.Is(err, store.ErrRecipeVersionNotPublished):
		// 409 而不是 422：请求本身合法，是对象状态不允许这个操作
		//（与「非法状态 409」的既有约定一致）。
		app.writeStudioError(w, r, studio.NewError(studio.CodeConflict, err.Error()))
		return
	}
	app.writeAPIEntityError(w, r, err)
}

// parseRecipeID 解析路径里的方案 ID。
//
// 非法 ID **自己写 404** 再返回 false：调用方拿到 false 就 return，
// 若这里不写响应，客户端会收到一个 200 + 空 body —— 那比 404 难排障得多
// （看起来像“服务器忘了返回内容”）。
func (app *application) parseRecipeID(w http.ResponseWriter, r *http.Request, param string) (int64, bool) {
	recipeID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue(param)), 10, 64)
	if err != nil || recipeID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该方案"))
		return 0, false
	}
	return recipeID, true
}

func parseInt64Query(r *http.Request, key string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get(key)), 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func parseIntQuery(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// derefRecipeVersionID 解引用可空的方案版本 ID（幂等摘要用 0 表示未指定）。
func derefRecipeVersionID(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
