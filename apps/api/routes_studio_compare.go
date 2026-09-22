package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件实现同基准比较的命令与读模型（Issue #160 T18）。
//
// 契约：docs/plans/atelier-implementation.md §3（P06）、internal/model/comparison.go。

func init() {
	RegisterRoutes(registerComparisonRoutes)
}

func registerComparisonRoutes(mux *http.ServeMux, app *application) {
	base := projectPrefix + "/{projectId}/comparison-baselines"
	mux.HandleFunc("POST "+base, app.createComparisonBaseline)
	mux.HandleFunc("GET "+base, app.listComparisonBaselines)
	mux.HandleFunc("GET "+base+"/{baselineId}", app.getComparisonBaseline)
	mux.HandleFunc("POST "+base+"/{baselineId}/adopt", app.adoptComparison)
	// 采用指针：`/runs/new` 用它预填版本（采用只更新指针，不自动运行）。
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/adopted-batch", app.getAdoptedBatch)
}

// comparisonBaselineRequest 是创建比较基准的请求体。
type comparisonBaselineRequest struct {
	Name          string            `json:"name"`
	Metric        string            `json:"metric"`
	InputRef      string            `json:"inputRef"`
	CoverageSlice map[string]any    `json:"coverageSlice"`
	SamplingSeed  int64             `json:"samplingSeed"`
	Rubric        model.RubricSpec  `json:"rubric"`
	Judges        []model.JudgeSpec `json:"judges"`
	LeftBatchID   int64             `json:"leftBatchId"`
	RightBatchID  int64             `json:"rightBatchId"`
}

// createComparisonBaseline 冻结一份比较前提（契约 §2.5 的固定范围思路）。
//
// 判定「可比」的规则在 model 层（`ValidateComparisonBaseline`），因此
// 「声明逐题配对却未固定输入」这类基准**根本进不了库**。
func (app *application) createComparisonBaseline(w http.ResponseWriter, r *http.Request) {
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
	// 创建比较基准需要运行权限：它决定「拿哪两个批次做比较」，
	// 而那是运行侧的决定（reviewer 只判断内容，不安排运行）。
	decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRun)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	var request comparisonBaselineRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}

	baseline, err := app.studio.Comparisons.CreateComparisonBaseline(r.Context(),
		store.CreateComparisonBaselineInput{
			ProjectID: projectID, InputRef: request.InputRef,
			CoverageSlice: request.CoverageSlice, SamplingSeed: request.SamplingSeed,
			Rubric: request.Rubric, Judges: request.Judges, Metric: request.Metric,
			LeftBatchID: request.LeftBatchID, RightBatchID: request.RightBatchID,
			Name: request.Name, CreatedBy: &user.ID,
		})
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	envelope := studio.NewEnvelope(
		strconv.FormatInt(baseline.ID, 10), "ready", 0, baseline.CreatedAt,
		model.ComparisonCapabilities(decision.Role, true, false),
		comparisonLinks(projectID, baseline.ID), nil,
	)
	envelope.Data = baseline
	app.writeStudioEnvelope(w, http.StatusCreated, envelope)
}

// getComparisonBaseline 返回基准 + 报告。
//
// 报告与基准一起返回（而不是分两个端点）：报告正是「这个基准能不能用」的
// 答案，分开请求会让界面在两秒内显示一个「可比」而另一个说不可比。
func (app *application) getComparisonBaseline(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, baselineID, ok := app.resolveBaselinePath(w, r, user.ID)
	if !ok {
		return
	}
	decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRead)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	baseline, err := app.studio.Comparisons.GetComparisonBaseline(r.Context(), projectID, baselineID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	report, err := app.studio.Comparisons.BuildComparisonReport(r.Context(), projectID, baselineID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	_, _, _, adopted, err := app.studio.Comparisons.AdoptedBatch(r.Context(), projectID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	envelope := studio.NewEnvelope(
		strconv.FormatInt(baseline.ID, 10), "ready", 0, baseline.CreatedAt,
		model.ComparisonCapabilities(decision.Role, report.Comparability.Comparable, adopted),
		comparisonLinks(projectID, baseline.ID), nil,
	)
	envelope.Data = struct {
		Baseline model.ComparisonBaseline `json:"baseline"`
		Report   model.ComparisonReport   `json:"report"`
		Adopted  bool                     `json:"adopted"`
	}{Baseline: baseline, Report: report, Adopted: adopted}
	app.writeStudioEnvelope(w, http.StatusOK, envelope)
}

// listComparisonBaselines 列出项目的比较基准。
func (app *application) listComparisonBaselines(w http.ResponseWriter, r *http.Request) {
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
	items, err := app.studio.Comparisons.ListComparisonBaselines(r.Context(), projectID, 50)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "nextCursor": "", "sortKey": "createdAt:desc",
	})
}

// adoptRequest 是采用请求体。
type adoptRequest struct {
	Side   string `json:"side"`
	Reason string `json:"reason"`
}

// adoptComparison 采用某一侧（只更新指针并记录依据）。
func (app *application) adoptComparison(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, baselineID, ok := app.resolveBaselinePath(w, r, user.ID)
	if !ok {
		return
	}
	// 采用是「决定接下来按哪个方案扩量」的动作，因此要求 owner。
	decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzPublish)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	if decision.Role != model.ProjectRoleOwner {
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden,
			"只有项目负责人可以采用方案"))
		return
	}

	var request adoptRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误", nil))
		return
	}

	baseline, adoptedBatchID, err := app.studio.Comparisons.Adopt(r.Context(), store.AdoptComparisonInput{
		ProjectID: projectID, BaselineID: baselineID, Side: request.Side,
		Reason: request.Reason, CreatedBy: &user.ID,
	})
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	envelope := studio.NewEnvelope(
		strconv.FormatInt(baseline.ID, 10), "ready", 0, baseline.CreatedAt,
		model.ComparisonCapabilities(decision.Role, true, true),
		comparisonLinks(projectID, baseline.ID), nil,
	)
	envelope.Data = map[string]any{
		"baseline":       baseline,
		"adoptedSide":    request.Side,
		"adoptedBatchId": adoptedBatchID,
		// 「采用只规划新批次」：给出扩量规划入口的链接与预填参数，
		// 但**不**自动创建批次（T18 验收项：不自动运行或发布）。
		"nextStep": map[string]any{
			"label": "按采用的方案规划扩量批次",
			"href":  "/p/" + strconv.FormatInt(projectID, 10) + "/runs/new",
			"prefill": map[string]any{
				"fromBatchId": adoptedBatchID,
				"baselineId":  baseline.ID,
			},
		},
	}
	app.writeStudioEnvelope(w, http.StatusAccepted, envelope)
}

// getAdoptedBatch 返回项目当前采用的批次（供 `/runs/new` 预填）。
func (app *application) getAdoptedBatch(w http.ResponseWriter, r *http.Request) {
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

	batchID, baselineID, side, found, err := app.studio.Comparisons.AdoptedBatch(r.Context(), projectID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	if !found {
		// 尚未采用不是错误：`/runs/new` 应当直接显示「没有采用方案」，
		// 而不是一个 404（那会让页面以为接口坏了）。
		app.writeJSON(w, http.StatusOK, map[string]any{"adopted": false})
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"adopted": true, "batchId": batchID, "baselineId": baselineID, "side": side,
	})
}

// comparisonLinks 是比较对象可跳转的链接。
func comparisonLinks(projectID, baselineID int64) studio.Links {
	base := projectPrefix + "/" + strconv.FormatInt(projectID, 10) +
		"/comparison-baselines/" + strconv.FormatInt(baselineID, 10)
	return studio.Links{
		"self":    base,
		"project": projectPrefix + "/" + strconv.FormatInt(projectID, 10),
		"page":    "/p/" + strconv.FormatInt(projectID, 10) + "/compare?baselineId=" + strconv.FormatInt(baselineID, 10),
	}
}

// resolveBaselinePath 解析并校验 `P/comparison-baselines/{baselineId}`。
func (app *application) resolveBaselinePath(w http.ResponseWriter, r *http.Request, userID int64) (int64, int64, bool) {
	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, msgProjectNotFound))
		return 0, 0, false
	}
	if _, err := app.studio.Authorize(r.Context(), projectID, userID, store.AuthzRead); err != nil {
		app.writeStudioError(w, r, err)
		return 0, 0, false
	}
	baselineID, err := strconv.ParseInt(r.PathValue("baselineId"), 10, 64)
	if err != nil || baselineID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该比较基准"))
		return 0, 0, false
	}
	return projectID, baselineID, true
}

var _ = errors.Is
