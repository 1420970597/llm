package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件实现质量实验的命令与读模型（Issue #160 T19 的数据面 + T14 的命令面）。
//
// 契约：docs/plans/atelier-api-contract.md §2.5（创建实验）、§3（报告）、§3.1（分列统计）。
//
// 决定把实验 API 放在这里而不是 T14 里：T14 交付的是「冻结与执行」的
// 数据与判据，而这个端点服务的是页面（`/quality`、`/quality/new`、
// `/quality/{id}`）。落点与 T19 的验收项一致。

func init() {
	RegisterRoutes(registerQualityRoutes)
}

func registerQualityRoutes(mux *http.ServeMux, app *application) {
	base := projectPrefix + "/{projectId}/experiments"
	mux.HandleFunc("POST "+base, app.createExperiment)
	mux.HandleFunc("GET "+base, app.listExperiments)
	mux.HandleFunc("GET "+base+"/{experimentId}", app.getExperiment)
}

// experimentRequest 是创建实验的请求体（契约 §2.5）。
type experimentRequest struct {
	SampleVersionIDs   []int64          `json:"sampleVersionIds"`
	SamplingSeed       int64            `json:"samplingSeed"`
	Rubric             model.RubricSpec `json:"rubric"`
	JudgeConnectionIDs []int64          `json:"judgeConnectionIds"`
	MissingScorePolicy string           `json:"missingScorePolicy"`
	BatchID            *int64           `json:"batchId"`
}

// createExperiment 冻结一个实验（契约 §2.5）。
//
// 三条拒绝路径（都由 store/判据层给出，这里只做映射）：
//   - 空范围 → 422（否则分母为 0 却声称有结论）；
//   - 没有独立裁判 → 422（同真实来源的别名连接不算独立裁判）；
//   - GRPO → 422 且原因点名 T24（用 SFT 量表跑 GRPO 会产出
//     「看起来正常、其实语义错误」的结论，比直接拒绝更危险）。
func (app *application) createExperiment(w http.ResponseWriter, r *http.Request) {
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
	decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRun)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	if decision.Project.TargetKind == model.TargetKindGRPO {
		app.writeStudioError(w, r, studio.NewValidationError(
			"GRPO 质量适配器由 T24 交付，在此之前 GRPO 项目不能创建质量实验",
			[]model.FieldError{{Field: "targetKind",
				Message: "GRPO 质量实验尚未接入（T24）"}}))
		return
	}

	var request experimentRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}

	// 裁判身份：前端只传**连接 ID**，而来源指纹由服务端从连接当前配置读取。
	// 不接受客户端传 fingerprint —— 那会让「同源自评」只需伪造一个指纹就能绕过
	//（T14 验收项明确要求独立性判定不可被客户端绕过）。
	judges, err := app.resolveJudgeSpecs(r, request.JudgeConnectionIDs)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	experiment, err := app.studio.Experiments.CreateExperiment(r.Context(), store.CreateExperimentInput{
		ProjectID: projectID, BatchID: request.BatchID, TargetKind: decision.Project.TargetKind,
		Purpose:      model.ExperimentPurposeQuality,
		SamplingSeed: request.SamplingSeed, SampleVersionIDs: request.SampleVersionIDs,
		Judges: judges, Rubric: request.Rubric,
		MissingScorePolicy: request.MissingScorePolicy, CreatedBy: &user.ID,
	})
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	envelope := studio.NewEnvelope(
		strconv.FormatInt(experiment.ID, 10), experiment.Status, 0, experiment.CreatedAt,
		model.ExperimentCapabilities(decision.Role, experiment.Status),
		experimentLinks(projectID, experiment.ID), nil,
	)
	envelope.Data = experiment
	// 202：实验已冻结但执行是异步的（T19 验收项「创建后排队页不提前显示最终分数」）。
	app.writeStudioEnvelope(w, http.StatusAccepted, envelope)
}

// resolveJudgeSpecs 把连接 ID 解析成裁判快照（指纹由服务端读取）。
func (app *application) resolveJudgeSpecs(r *http.Request, connectionIDs []int64) ([]model.JudgeSpec, error) {
	judges := make([]model.JudgeSpec, 0, len(connectionIDs))
	for _, connectionID := range connectionIDs {
		if connectionID <= 0 {
			continue
		}
		// 用连接的非秘密信息与 endpoint 构造指纹；凭证不进快照（§2.4）。
		baseURL, modelName, _, _, _, err := app.datasets.ResolveProvider(r.Context(), connectionID)
		if err != nil {
			return nil, studio.NewValidationError(
				"裁判连接不存在或不可用，请到连接设置确认",
				[]model.FieldError{{Field: "judgeConnectionIds",
					Message: "所选裁判连接不可用"}})
		}
		judges = append(judges, model.JudgeSpec{
			ConnectionID: connectionID, Label: modelName,
			EndpointFingerprint: model.EndpointFingerprint(baseURL), ModelName: modelName,
		})
	}
	if len(judges) == 0 {
		return nil, studio.NewValidationError("实验至少需要一名裁判",
			[]model.FieldError{{Field: "judgeConnectionIds", Message: "请选择至少一名裁判连接"}})
	}
	return judges, nil
}

// listExperiments 列出项目的实验。
func (app *application) listExperiments(w http.ResponseWriter, r *http.Request) {
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
	items, err := app.studio.Experiments.ListExperiments(r.Context(), projectID, 50)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "nextCursor": "", "sortKey": "createdAt:desc",
	})
}

// getExperiment 返回实验 + 报告（契约 §3 的 Q03）。
//
// 报告的**分母是冻结的 inspectedCount**，与实验一起返回，
// 因此「刷新/分享能恢复同一实验」不依赖内存里的 selectedRunId（T19 验收项）。
func (app *application) getExperiment(w http.ResponseWriter, r *http.Request) {
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
	experimentID, err := strconv.ParseInt(r.PathValue("experimentId"), 10, 64)
	if err != nil || experimentID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该实验"))
		return
	}

	experiment, err := app.studio.Experiments.GetExperiment(r.Context(), experimentID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// 跨项目实验按「不存在」处理（不确认它存在）。
	if experiment.ProjectID != projectID {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该实验"))
		return
	}
	report, err := app.studio.Experiments.BuildExperimentReport(r.Context(), experimentID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// 待判断项：报告只给结论，而队列页需要「下一步看哪几条」。
	pending, err := app.studio.Experiments.ListPendingExperimentItems(r.Context(), experimentID, 50)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	// 信封的 revision 用**冻结的分母**（inspectedCount）：它是这个实验最稳定的
	// 版本标识，比「判断次数」更能表达「这份报告基于多大的范围」。
	envelope := studio.NewEnvelope(
		strconv.FormatInt(experiment.ID, 10), experiment.Status, int64(experiment.InspectedCount),
		experiment.UpdatedAt, model.ExperimentCapabilities(decision.Role, experiment.Status),
		experimentLinks(projectID, experiment.ID), nil,
	)
	// 报告自带分列统计（§3.1）：接纳率的分母是**冻结的 inspected**，
	// 零分母时显示「无结论」而不是 100%。不再包一层视图类型 ——
	// 那会让同一个 stats 在响应里出现两次，而两份字段必然有一天不一致。
	envelope.Data = struct {
		Experiment model.Experiment       `json:"experiment"`
		Report     store.ExperimentReport `json:"report"`
		Pending    []model.ExperimentItem `json:"pendingItems"`
	}{Experiment: experiment, Report: report, Pending: pending}
	app.writeStudioEnvelope(w, http.StatusOK, envelope)
}

// experimentLinks 是实验相关对象可跳转的链接。
func experimentLinks(projectID, experimentID int64) studio.Links {
	base := projectPrefix + "/" + strconv.FormatInt(projectID, 10) +
		"/experiments/" + strconv.FormatInt(experimentID, 10)
	return studio.Links{
		"self":    base,
		"project": projectPrefix + "/" + strconv.FormatInt(projectID, 10),
		// 页面链接（前端不自行拼 URL）。
		"page": "/p/" + strconv.FormatInt(projectID, 10) + "/quality/" + strconv.FormatInt(experimentID, 10),
	}
}
