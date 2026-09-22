package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5"
)

// 本文件实现人工判断、分派、冲突协调与大范围选择快照的 HTTP 命令
// （Issue #160 T16 的落点 + T17 的选择快照）。
//
// 契约：docs/plans/atelier-api-contract.md §2.7（记录判断）、§3.2（`selection`）。

func init() {
	RegisterRoutes(registerReviewRoutes)
}

// registerReviewRoutes 注册判断与选择相关路由。
//
// 判断挂在**具体内容版本**下面（`/samples/{sampleId}/versions/{version}/decisions`）：
// 判断的对象是某一版内容，而不是「这个样本」—— 把版本放进路径让这件事
// 在 URL 上就成立，避免「判了旧版本却以为是新版本」。
func registerReviewRoutes(mux *http.ServeMux, app *application) {
	base := projectPrefix + "/{projectId}/samples/{sampleId}/versions/{version}"
	mux.HandleFunc("POST "+base+"/decisions", app.submitDecision)
	mux.HandleFunc("GET "+base+"/decisions", app.listDecisions)
	mux.HandleFunc("POST "+base+"/resolve-conflict", app.resolveReviewConflict)
	mux.HandleFunc("POST "+base+"/assignments", app.assignReview)

	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/review-assignments", app.listReviewAssignments)

	// 大范围选择快照：URL 只带快照 ID，ID 列表留在服务端。
	mux.HandleFunc("POST "+projectPrefix+"/{projectId}/selection-snapshots", app.createSelectionSnapshot)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/selection-snapshots/{snapshotId}", app.getSelectionSnapshot)
}

// decisionRequest 是记录判断的请求体（契约 §2.7）。
type decisionRequest struct {
	EvidenceRevision int64  `json:"evidenceRevision"`
	ReviewerRevision int64  `json:"reviewerRevision"`
	Action           string `json:"action"`
	Reason           string `json:"reason"`
	Supersedes       *int64 `json:"supersedes"`
}

// decisionLinks 是判断相关对象可跳转的链接。
func decisionLinks(projectID, sampleID int64, version int) studio.Links {
	base := projectPrefix + "/" + strconv.FormatInt(projectID, 10) +
		"/samples/" + studio.SampleResourceID(sampleID) + "/versions/" + strconv.Itoa(version)
	return studio.Links{
		"self":    base + "/decisions",
		"content": base,
		"history": projectPrefix + "/" + strconv.FormatInt(projectID, 10) + "/samples/" +
			studio.SampleResourceID(sampleID) + "/history",
	}
}

// submitDecision 记录一条判断（契约 §2.7）。
//
// 三种必须区分的失败：
//   - 旧序号（同一人并发更正）→ 409，且**保留用户输入**（客户端据此把
//     理由留在表单里，而不是清空）；
//   - 旧证据版本 → 409（必须基于新证据重新判断）；
//   - 权限不足 → 403/404。
//
// 把它们压成同一个「提交失败」会让用户无从下手：一个要重新加载、
// 一个要补证据、一个要找负责人。
func (app *application) submitDecision(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, sample, version, ok := app.resolveVersionPath(w, r, user.ID)
	if !ok {
		return
	}
	if _, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzReview); err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	var request decisionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}

	result, err := app.studio.Reviews.SubmitDecision(r.Context(), projectID, user.ID,
		model.SubmitDecisionInput{
			SampleVersionID: version.ID, EvidenceRevision: request.EvidenceRevision,
			ReviewerRevision: request.ReviewerRevision, Action: request.Action,
			Reason: request.Reason, Supersedes: request.Supersedes,
		})
	if err != nil {
		app.writeReviewCommandError(w, r, err)
		return
	}

	envelope := studio.NewEnvelope(
		studio.SampleResourceID(sample.ID)+"/v"+strconv.Itoa(version.Version), "ready",
		result.Projection.AggregateReviewRevision, version.CreatedAt,
		model.ReviewProjectionCapabilities(model.ProjectRoleOwner, result.Projection.EffectiveAction),
		decisionLinks(projectID, sample.ID, version.Version), nil,
	)
	envelope.Data = struct {
		Decision   model.ReviewDecision   `json:"decision"`
		Projection model.ReviewProjection `json:"projection"`
		// Blockers 让前端在保存后立刻知道「这一条还差什么」（T20 复用同一份判定）。
		Blockers []studio.Blocker `json:"blockers"`
	}{
		Decision: result.Decision, Projection: result.Projection,
		Blockers: studio.ReviewBlockers(studio.SampleVersionRef{
			ProjectID: projectID, SampleID: sample.ID, Version: version.Version,
		}, result.Projection),
	}
	app.writeStudioEnvelope(w, http.StatusCreated, envelope)
}

// listDecisions 列出某内容版本的**全部**判断（含被取代的）。
//
// 返回全部而不是只返回有效的：界面要能显示「原来判过什么、后来谁更正了」，
// 而那正是 supersedes 链的价值（纠错后旧证据与操作者可追溯）。
func (app *application) listDecisions(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, _, version, ok := app.resolveVersionPath(w, r, user.ID)
	if !ok {
		return
	}
	if _, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRead); err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	decisions, err := app.studio.Reviews.ListDecisions(r.Context(), projectID, version.ID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	projection, err := app.studio.Reviews.GetProjection(r.Context(), projectID, version.ID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": decisions,
		// 投影与判断一起返回：分开请求会让「有效处置」与「判断历史」
		// 有短暂的窗口不一致，而用户会看到「两条相反判断但没有冲突标记」。
		"projection": projection,
		"sortKey":    "id:asc",
	})
}

// resolveReviewConflict 追加协调决定（项目 owner 专用）。
func (app *application) resolveReviewConflict(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, sample, version, ok := app.resolveVersionPath(w, r, user.ID)
	if !ok {
		return
	}
	// 协调是**决定最终结论**的动作，因此要求 owner（不是 reviewer：
	// 相反的意见正是 reviewer 表达意见的产物）。
	decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzPublish)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	if decision.Role != model.ProjectRoleOwner {
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden,
			"只有项目负责人可以追加协调决定"))
		return
	}

	var request decisionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}

	result, err := app.studio.Reviews.ResolveConflict(r.Context(), projectID, user.ID, version.ID,
		request.Action, request.Reason, request.Supersedes)
	if err != nil {
		app.writeReviewCommandError(w, r, err)
		return
	}
	envelope := studio.NewEnvelope(
		studio.SampleResourceID(sample.ID)+"/v"+strconv.Itoa(version.Version), "ready",
		result.Projection.AggregateReviewRevision, version.CreatedAt,
		model.ReviewProjectionCapabilities(decision.Role, result.Projection.EffectiveAction),
		decisionLinks(projectID, sample.ID, version.Version), nil,
	)
	envelope.Data = result
	app.writeStudioEnvelope(w, http.StatusCreated, envelope)
}

// assignRequest 是分派请求体。
type assignRequest struct {
	RiskKey    string `json:"riskKey"`
	AssigneeID *int64 `json:"assigneeId"`
	Note       string `json:"note"`
}

// assignReview 分派或重新分派待办。
func (app *application) assignReview(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, sample, version, ok := app.resolveVersionPath(w, r, user.ID)
	if !ok {
		return
	}
	if _, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzReview); err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	var request assignRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误", nil))
		return
	}
	assignment, err := app.studio.Reviews.AssignReview(r.Context(), store.AssignReviewInput{
		ProjectID: projectID, SampleVersionID: version.ID, RiskKey: request.RiskKey,
		AssigneeID: request.AssigneeID, AssignedBy: &user.ID, Note: request.Note,
	})
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	_ = sample
	app.writeJSON(w, http.StatusCreated, map[string]any{
		"assignment": assignment,
		"links":      decisionLinks(projectID, sample.ID, version.Version),
	})
}

// listReviewAssignments 列出待办。
func (app *application) listReviewAssignments(w http.ResponseWriter, r *http.Request) {
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
	if _, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRead); err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	query, err := studio.ParseListQuery(r.URL.Query(), 50)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// `mine=1` 只看分派给我的：审阅者最常问的是「我该看哪些」。
	assigneeID := int64(0)
	if r.URL.Query().Get("mine") == "1" {
		assigneeID = user.ID
	}
	assignments, err := app.studio.Reviews.ListAssignments(r.Context(), projectID, assigneeID, "", query.Limit)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": assignments, "nextCursor": "", "sortKey": "id:asc",
	})
}

// selectionSnapshotRequest 是创建选择快照的请求体。
type selectionSnapshotRequest struct {
	Purpose string `json:"purpose"`
	// SampleVersionIDs 是**少量**显式选择的版本（小范围选择走这条路）。
	SampleVersionIDs []int64 `json:"sampleVersionIds"`
	// FromFilter 表示「按筛选条件全选」：由服务端解析成具体 ID 并冻结，
	// 因此 URL 里不会出现数万 ID（契约 §3.2）。
	FromFilter *struct {
		ReviewStatus string `json:"reviewStatus"`
		BatchID      *int64 `json:"batchId"`
		Search       string `json:"search"`
	} `json:"fromFilter"`
}

// createSelectionSnapshot 冻结一份选择（发布候选/实验/导出的范围）。
func (app *application) createSelectionSnapshot(w http.ResponseWriter, r *http.Request) {
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
	if _, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRead); err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	var request selectionSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误", nil))
		return
	}

	versionIDs := request.SampleVersionIDs
	if request.FromFilter != nil {
		// 按筛选条件在**服务端**解析成具体 ID：前端只传条件，
		// 因此不存在「客户端可改的 ID 列表」这一回事。
		resolved, err := app.studio.Batches.ListSampleVersionIDsByFilter(r.Context(), store.SampleVersionFilter{
			ProjectID:    projectID,
			ReviewStatus: request.FromFilter.ReviewStatus,
			BatchID:      request.FromFilter.BatchID,
			Search:       request.FromFilter.Search,
		}, store.MaxSelectionSnapshotItems)
		if err != nil {
			app.writeStudioError(w, r, err)
			return
		}
		versionIDs = resolved
	}
	if len(versionIDs) == 0 {
		app.writeStudioError(w, r, studio.NewValidationError(
			"选择范围为空：没有可发布的样本版本", nil))
		return
	}

	snapshot, err := app.studio.Selections.Create(r.Context(), store.CreateSelectionSnapshotInput{
		ProjectID: projectID, Purpose: request.Purpose, CreatedBy: &user.ID,
		SampleVersionIDs: versionIDs,
		Filter:           selectionFilterJSON(request.FromFilter),
	})
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	envelope := studio.NewEnvelope(
		strconv.FormatInt(snapshot.ID, 10), "ready", 0, snapshot.CreatedAt,
		model.Capabilities{CanEdit: true, CanDownload: true},
		studio.Links{
			"self": projectPrefix + "/" + strconv.FormatInt(projectID, 10) +
				"/selection-snapshots/" + strconv.FormatInt(snapshot.ID, 10),
		}, nil,
	)
	envelope.Data = snapshot
	app.writeStudioEnvelope(w, http.StatusCreated, envelope)
}

// getSelectionSnapshot 解析一份选择快照。
//
// **重新鉴权**（T17 验收项：「少量 selection 参数也需重新鉴权」）：
// 快照 ID 可以出现在 URL 里，因此它必须是一次真实的授权检查点 ——
// 拿别人的快照 ID 不能读到别人的内容范围。
func (app *application) getSelectionSnapshot(w http.ResponseWriter, r *http.Request) {
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
	if _, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRead); err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	snapshotID, err := strconv.ParseInt(r.PathValue("snapshotId"), 10, 64)
	if err != nil || snapshotID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该选择范围"))
		return
	}
	snapshot, items, err := app.studio.Selections.Get(r.Context(), projectID, snapshotID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"snapshot": snapshot,
		"items":    items,
		"count":    len(items),
	})
}

// writeReviewCommandError 把判断命令的错误翻译成契约错误码。
func (app *application) writeReviewCommandError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrReviewerRevisionStale):
		// 409 + 明确说明「请保留理由重新提交」：客户端的草稿不能被清掉，
		// 而用户需要知道这不是他填错了。
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeRevisionStale,
			"你的判断已被同一账号的另一次提交取代；已为你保留填写的理由，请刷新后重新提交", nil)
	case errors.Is(err, store.ErrEvidenceRevisionStale):
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeRevisionStale,
			"必需证据集已变化，请基于最新证据重新判断后再提交", nil)
	case errors.Is(err, store.ErrComparisonBaselineNotFound):
		app.writeAPIError(w, r, http.StatusNotFound, studio.CodeNotFound,
			"未找到该比较基准，请返回比较页刷新后重试", nil)
	case errors.Is(err, store.ErrReviewConflictUnresolved):
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeConflict, err.Error(), nil)
	case errors.Is(err, pgx.ErrNoRows):
		app.writeAPIError(w, r, http.StatusNotFound, studio.CodeNotFound, msgProjectNotFound, nil)
	default:
		app.writeStudioError(w, r, err)
	}
}

// resolveVersionPath 解析 `P/samples/{sampleId}/versions/{version}` 并校验归属。
func (app *application) resolveVersionPath(w http.ResponseWriter, r *http.Request, userID int64) (int64, model.Sample, model.SampleVersion, bool) {
	projectID, sample, ok := app.resolveSamplePath(w, r, userID)
	if !ok {
		return 0, model.Sample{}, model.SampleVersion{}, false
	}
	versionNumber, err := parseVersionSegment(r.PathValue("version"))
	if err != nil {
		app.writeStudioError(w, r, err)
		return 0, model.Sample{}, model.SampleVersion{}, false
	}
	version, err := app.studio.Batches.GetSampleVersion(r.Context(), projectID, sample.ID, versionNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound,
				"未找到该内容版本，请返回样本列表刷新后重试"))
			return 0, model.Sample{}, model.SampleVersion{}, false
		}
		app.writeStudioError(w, r, err)
		return 0, model.Sample{}, model.SampleVersion{}, false
	}
	return projectID, sample, version, true
}

// selectionFilterJSON 把筛选条件序列化成快照的解释字段。
func selectionFilterJSON(filter *struct {
	ReviewStatus string `json:"reviewStatus"`
	BatchID      *int64 `json:"batchId"`
	Search       string `json:"search"`
}) json.RawMessage {
	if filter == nil {
		return json.RawMessage(`{"source":"explicit"}`)
	}
	raw, err := json.Marshal(map[string]any{
		"source": "filter", "reviewStatus": filter.ReviewStatus,
		"batchId": filter.BatchID, "search": filter.Search,
	})
	if err != nil {
		return json.RawMessage(`{"source":"filter"}`)
	}
	return raw
}
