package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 Atelier 版本化文档 API（Issue #160 T04）。
//
// 契约：docs/plans/atelier-api-contract.md §2.2。
//
// 五类文档共用同一套命令形状（POST P/<kind>-versions），因为它们的生命周期
// 完全相同：保存产生新版本、旧版本只读、expectedRevision 不匹配 409。
// payload 的 typed 差异由 internal/model/studio_docs.go 的校验承载。

// documentPathSegment 把文档类型映射到 URL 段（契约 §2.2 的拼写）。
func documentPathSegment(kind model.DocumentKind) string {
	switch kind {
	case model.KindBlueprint:
		return "blueprint-versions"
	case model.KindCoverage:
		return "coverage-versions"
	case model.KindStandard:
		return "standard-versions"
	case model.KindQualityPolicy:
		return "quality-policy-versions"
	case model.KindMapping:
		return "mapping-versions"
	default:
		return ""
	}
}

// documentKindFromSegment 反向解析 URL 段。
func documentKindFromSegment(segment string) (model.DocumentKind, bool) {
	for _, kind := range model.AllDocumentKinds() {
		if documentPathSegment(kind) == segment {
			return kind, true
		}
	}
	return "", false
}

func init() {
	RegisterRoutes(registerDocumentRoutes)
}

// registerDocumentRoutes 注册五类版本化文档的路由。
//
// 用一次循环注册而不是手写 10 条 `mux.HandleFunc`：段名与类型必须一一对应，
// 循环让「新增一类文档忘记注册某个动词」不可能发生（漏的是整类，启动即 404 可测）。
func registerDocumentRoutes(mux *http.ServeMux, app *application) {
	for _, kind := range model.AllDocumentKinds() {
		segment := documentPathSegment(kind)
		base := projectPrefix + "/{projectId}/" + segment
		mux.HandleFunc("GET "+base, app.listDocumentVersions(kind))
		mux.HandleFunc("POST "+base, app.saveDocumentVersion(kind))
		mux.HandleFunc("GET "+base+"/{version}", app.getDocumentVersion(kind))
	}
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/documents", app.listDocuments)
}

// documentVersionRequest 是保存版本的请求体（契约 §2.2）。
//
// Payload 用 json.RawMessage 而不是具体类型：类型由 URL 段决定，
// 在这里解码成具体类型需要 switch；先取原始字节再分派能保证
// 「段与 payload 结构不匹配」返回的是字段级 422 而不是解析失败的 400。
type documentVersionRequest struct {
	ExpectedRevision *int64          `json:"expectedRevision"`
	LogicalID        string          `json:"logicalId"`
	ChangeReason     string          `json:"changeReason"`
	Payload          json.RawMessage `json:"payload"`
}

// saveDocumentVersion 处理 POST P/<kind>-versions。
func (app *application) saveDocumentVersion(kind model.DocumentKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project, decision, ok := app.requireProject(w, r, store.AuthzDesign)
		if !ok {
			return
		}
		user, _ := requestUser(r)

		var input documentVersionRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			app.writeAPIError(w, r, http.StatusBadRequest, codeValidation,
				"请求格式有误，请检查填写的内容后重试", nil)
			return
		}

		payload, fieldErrors := decodeDocumentPayload(kind, input.Payload)
		if len(fieldErrors) > 0 {
			app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation,
				"文档内容格式不正确", fieldErrors)
			return
		}

		expected := int64(0)
		if input.ExpectedRevision != nil {
			expected = *input.ExpectedRevision
		}

		document, version, err := app.documents.SaveVersion(r.Context(), project.ID, kind, user.ID, store.SaveDocumentVersionInput{
			ExpectedRevision: expected,
			LogicalID:        input.LogicalID,
			ChangeReason:     input.ChangeReason,
			Payload:          payload,
		})
		if err != nil {
			app.writeDocumentError(w, r, err)
			return
		}

		app.writeJSON(w, http.StatusCreated, apiEnvelope{
			ID:           documentVersionResourceID(version.ID),
			Status:       "saved",
			Revision:     document.RowVersion,
			UpdatedAt:    version.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			Capabilities: model.ProjectCapabilities(decision.Role, project.Status),
			Links:        app.documentLinks(project.ID, kind, document.LogicalID, version.Version),
			Warnings:     []string{},
			Data: map[string]any{
				"document": document,
				"version":  version,
			},
		})
	}
}

// listDocumentVersions 处理 GET P/<kind>-versions。
func (app *application) listDocumentVersions(kind model.DocumentKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project, decision, ok := app.requireProject(w, r, store.AuthzRead)
		if !ok {
			return
		}
		logicalID := r.URL.Query().Get("logicalId")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		versions, err := app.documents.ListVersions(r.Context(), project.ID, kind, logicalID, limit)
		if err != nil {
			app.writeDocumentError(w, r, err)
			return
		}

		// 头记录可能还不存在（用户从没保存过）——那不是错误：
		// 「还没有版本」是合法初态，界面应显示空态而不是报错。
		current, err := app.documents.GetDocument(r.Context(), project.ID, kind, logicalID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			app.writeDocumentError(w, r, err)
			return
		}

		body := map[string]any{
			"items":     versions,
			"kind":      kind,
			"logicalId": normalizeLogicalID(logicalID),
			// canEdit 来自 requireProject 已经算出的决定（含归档判断），
			// 不在这里重算一次 —— 重算会漏掉归档这类「状态相关的拒绝」。
			"canEdit": model.ProjectCapabilities(decision.Role, project.Status).CanEdit,
			"sortKey": "version:desc",
		}
		if current.ID != 0 {
			body["document"] = current
		}
		app.writeJSON(w, http.StatusOK, body)
	}
}

// getDocumentVersion 处理 GET P/<kind>-versions/{version}。
//
// 历史版本只读（§2.2 验收项），因此这里只返回内容，不返回任何写链接。
func (app *application) getDocumentVersion(kind model.DocumentKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		project, _, ok := app.requireProject(w, r, store.AuthzRead)
		if !ok {
			return
		}
		versionNumber, err := strconv.Atoi(strings.TrimSpace(r.PathValue("version")))
		if err != nil || versionNumber <= 0 {
			app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation,
				"版本号不正确", []model.FieldError{{Field: "version", Message: "必须是正整数"}})
			return
		}

		document, err := app.documents.GetDocument(r.Context(), project.ID, kind, r.URL.Query().Get("logicalId"))
		if err != nil {
			app.writeDocumentError(w, r, err)
			return
		}
		version, err := app.documents.GetVersion(r.Context(), document.ID, versionNumber)
		if err != nil {
			app.writeDocumentError(w, r, err)
			return
		}
		references, err := app.documents.DocumentReferences(r.Context(), version.ID)
		if err != nil {
			app.writeDocumentError(w, r, err)
			return
		}

		app.writeJSON(w, http.StatusOK, map[string]any{
			"document":   document,
			"version":    version,
			"references": references,
			// 只读标记：历史版本不可修改，这是契约而不是界面约定。
			"readOnly": version.Version != document.CurrentVersion,
		})
	}
}

// listDocuments 处理 GET P/documents（项目内全部逻辑文档）。
func (app *application) listDocuments(w http.ResponseWriter, r *http.Request) {
	project, _, ok := app.requireProject(w, r, store.AuthzRead)
	if !ok {
		return
	}
	documents, err := app.documents.ListDocuments(r.Context(), project.ID)
	if err != nil {
		app.writeDocumentError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{"items": documents})
}

// decodeDocumentPayload 按类型解码 payload 并做**结构**级校验。
//
// typed 语义校验在 store 的 SaveVersion 里统一执行（那里也是方案复制 T26 与
// 批次快照 T05 的入口）。这里只负责「JSON 能不能解成这个类型的结构」，
// 因为那是本层唯一能比 store 更早发现的问题，而早发现的收益是错误字段更准确
// （JSON 结构错误指向具体字段；到了 store 只剩「内容格式不正确」）。
func decodeDocumentPayload(kind model.DocumentKind, raw json.RawMessage) (any, []model.FieldError) {
	if len(raw) == 0 {
		return nil, []model.FieldError{{Field: "payload", Message: "必填"}}
	}
	switch kind {
	case model.KindBlueprint:
		var payload model.BlueprintPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, jsonFieldErrors(err, "payload")
		}
		return payload, nil
	case model.KindCoverage:
		var payload model.CoveragePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, jsonFieldErrors(err, "payload")
		}
		return payload, nil
	case model.KindStandard:
		var payload model.StandardPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, jsonFieldErrors(err, "payload")
		}
		return payload, nil
	case model.KindQualityPolicy:
		var payload model.QualityPolicyPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, jsonFieldErrors(err, "payload")
		}
		return payload, nil
	case model.KindMapping:
		var payload model.MappingPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, jsonFieldErrors(err, "payload")
		}
		return payload, nil
	default:
		return nil, []model.FieldError{{Field: "kind", Message: "不支持的文档类型"}}
	}
}

// jsonFieldErrors 把 json 解码错误转成字段级错误。
//
// 只回显字段路径，不回显 Go 的类型名：`json: cannot unmarshal string into Go
// struct field BlueprintPayload.Nodes.Generation.Concurrency of type int`
// 对用户毫无用处，而 `payload.nodes.generation.concurrency 类型不正确` 能定位。
func jsonFieldErrors(err error, prefix string) []model.FieldError {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return []model.FieldError{{
			Field:   prefix + "." + typeErr.Field,
			Message: "类型不正确",
		}}
	}
	return []model.FieldError{{Field: prefix, Message: "格式不正确"}}
}

// writeDocumentError 把文档命令的错误翻译成契约响应。
func (app *application) writeDocumentError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrRevisionConflict) {
		// 409 + 明确文案：用户能做的事是「重新加载后再保存」（§1.4）。
		// 关键要求：前端必须**保留用户草稿**（契约 §1.4），
		// 因此响应不包含任何「请重填」的暗示。
		app.writeAPIError(w, r, http.StatusConflict, codeRevisionStale, err.Error(), nil)
		return
	}
	if fieldErrors, ok := model.HasFieldErrors(err); ok {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, err.Error(), fieldErrors)
		return
	}
	if store.IsStoreValidationError(err) {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, codeValidation, err.Error(), nil)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		app.writeAPIError(w, r, http.StatusNotFound, codeNotFound, "未找到该文档版本", nil)
		return
	}
	app.writeAPIEntityError(w, r, err)
}

// documentVersionResourceID 是版本对象的稳定 ID 形态。
//
// 与项目 ID 一样带前缀：`dv_12` 让「把版本行 ID 当成版本号」这类错误立刻可见
// （版本号是 `version`，行 ID 是 `id`，两者含义不同且都会出现在 URL/请求里）。
func documentVersionResourceID(id int64) string { return "dv_" + strconv.FormatInt(id, 10) }

// normalizeLogicalID 归一化逻辑文档 ID。
func normalizeLogicalID(logicalID string) string {
	trimmed := strings.TrimSpace(logicalID)
	if trimmed == "" {
		return store.DefaultLogicalID
	}
	return trimmed
}

// documentLinks 构造文档版本的可跳转链接（契约 §1.1：前端不自行拼 URL）。
//
// 同时给出 API 相对路径与页面路由：前者用于后续 API 调用，后者用于
// 面包屑与「在界面中打开」。两者都由服务端给出，避免前端各自拼一份
// 而漂移出「点了跳到 404」。
func (app *application) documentLinks(projectID int64, kind model.DocumentKind, logicalID string, version int) apiLinks {
	projectResource := projectResourceID(projectID)
	apiBase := projectPrefix + "/" + projectResource + "/" + documentPathSegment(kind)
	// 页面路由按 §3 的路由清单：蓝图/覆盖/标准各有独立页面，
	// 质量策略在 /rules，映射在发布准备页内展示。
	pagePath := map[model.DocumentKind]string{
		model.KindBlueprint:     "/p/" + projectResource + "/blueprint",
		model.KindCoverage:      "/p/" + projectResource + "/coverage",
		model.KindStandard:      "/p/" + projectResource + "/standard",
		model.KindQualityPolicy: "/p/" + projectResource + "/rules",
		model.KindMapping:       "/p/" + projectResource + "/releases/new",
	}[kind]

	links := apiLinks{
		"self":    apiBase + "/" + strconv.Itoa(version),
		"project": projectPrefix + "/" + projectResource,
	}
	if pagePath != "" {
		// ?version= 让「历史版本可分享」成立（§3.2 的 URL 参数契约）。
		links["page"] = pagePath + "?version=" + strconv.Itoa(version)
	}
	if logicalID != "" && logicalID != store.DefaultLogicalID {
		links["self"] += "?logicalId=" + logicalID
	}
	return links
}
