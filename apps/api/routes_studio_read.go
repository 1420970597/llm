package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 Atelier 的项目概览与样本读模型（Issue #160 T08，契约 §3、§3.1）。
//
// 读模型的共同要求：
//
//  1. **服务端分页**（契约 §1.5）：稳定游标、不含 offset。offset 在并发写入下
//     会漏行或重复行，而样本列表正是「边跑边看」的场景。
//  2. **分列统计不相互冒充**（契约 §3.1）：计划量与实际产出分开；
//     零分母显示「无结论」而不是 100%。
//  3. **读模型不按最大 ID 猜「当前」**（契约 §3）：概览返回的是计数与
//     最新版本摘要，而不是「ID 最大的那个批次就是当前运行」。

// ---------------------------------------------------------------------------
// GET P/overview（契约 §3）
// ---------------------------------------------------------------------------

// projectOverview 返回项目概览。
func (app *application) projectOverview(w http.ResponseWriter, r *http.Request) {
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

	overview, err := app.studio.LoadProjectOverview(r.Context(), projectID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	// 统计：计划量来自项目的 n/m/x，实际产出按**样本版本**计。
	// 两者都返回，但绝不合成一个「完成度」—— 合成会让「生成了 231 条
	// 但一条都没审」看起来像 60% 完成（契约 §3.1 明确禁止）。
	stats := studio.SampleStats{
		PlannedQuestions: studio.PlannedQuestions(
			decision.Project.DomainCount,
			decision.Project.DirectionsPerDomain,
			decision.Project.QuestionsPerDirection),
	}
	if generated, err := app.studio.Batches.CountSampleVersionsByProject(r.Context(), projectID); err == nil {
		stats.Generated = generated
	} else {
		app.logInternal(r, "count sample versions failed", err)
	}
	if samples, err := app.studio.Batches.CountSamplesByProject(r.Context(), projectID); err == nil {
		stats.StructureValid = samples
	} else {
		app.logInternal(r, "count samples failed", err)
	}
	// 纳入检查（inspected）由实验结果定义（T14），因此现在保持 0 且
	// 接纳率显示「无结论」—— 伪造一个分母会让「可以发布」看起来成立。
	rate, display := studio.AcceptanceRateOf(stats.Accepted, stats.Inspected)
	stats.AcceptanceRate = rate
	stats.AcceptanceRateDisplay = display

	envelope := studio.NewEnvelope(
		projectResourceID(projectID), decision.Project.Status, decision.Project.RowVersion,
		decision.Project.UpdatedAt,
		model.ProjectCapabilities(decision.Role, decision.Project.Status),
		studio.Links{
			"self":      projectPrefix + "/" + strconv.FormatInt(projectID, 10),
			"overview":  projectPrefix + "/" + strconv.FormatInt(projectID, 10) + "/overview",
			"blueprint": "/p/" + strconv.FormatInt(projectID, 10) + "/blueprint",
		},
		nil,
	)
	envelope.Data = struct {
		studio.ProjectOverview
		Stats studio.SampleStats `json:"stats"`
	}{ProjectOverview: overview, Stats: stats}
	app.writeStudioEnvelope(w, http.StatusOK, envelope)
}

// ---------------------------------------------------------------------------
// GET P/samples（契约 §3）
// ---------------------------------------------------------------------------

// sampleSummary 是样本列表项。
//
// 只放**样本身份**信息，不放内容：列表页不需要正文，而把正文塞进列表
// 会让一个 100 行的响应变成几兆（§5「不把大段样本内容塞进消息总线」的同一原则）。
type sampleSummary struct {
	SampleID      int64  `json:"sampleId"`
	ResourceID    string `json:"resourceId"`
	SampleKey     string `json:"sampleKey"`
	Title         string `json:"title"`
	TargetKind    string `json:"targetKind"`
	LatestVersion int    `json:"latestVersion"`
	// LatestVersionID 是当前指针对应的 sample_versions.id。命令 API（实验、
	// 发布、规则预览）接受的是这个行 ID，而不是样本身份或样本内版本号。
	LatestVersionID int64  `json:"latestVersionId"`
	OriginBatchID   *int64 `json:"originBatchId,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	// 审阅投影随列表一起返回（T17）：队列页要显示「哪些待审」，
	// 逐条查会变成 N+1 次请求，而队列正是「一次看一屏」的场景。
	ReviewStatus            string                    `json:"reviewStatus"`
	AggregateReviewRevision int64                     `json:"aggregateReviewRevision"`
	ReviewConflict          bool                      `json:"reviewConflict"`
	Capabilities            studio.SampleCapabilities `json:"capabilities"`
}

// toSampleSummary 转换样本（不含审阅投影）。
//
// 审阅状态由调用方通过 applyReviewProjection 补齐，而不是在这里填一个默认值：
// 单样本详情页若把「未加载」显示成「待判断」，用户会以为这一条还没人看过 ——
// 而事实可能是已被接纳。默认值在这里是有害的，因此刻意不设。
func toSampleSummary(sample model.Sample) sampleSummary {
	return sampleSummary{
		SampleID:      sample.ID,
		ResourceID:    studio.SampleResourceID(sample.ID),
		SampleKey:     sample.SampleKey,
		Title:         sample.Title,
		TargetKind:    sample.TargetKind,
		LatestVersion: sample.LatestVersion,
		// 详情路径只携带样本身份；调用方若需要精确版本 ID，应使用列表读模型
		// 或详情中的 version.versionId。这里保持零值，避免按版本号猜行 ID。
		OriginBatchID: sample.OriginBatchID,
		CreatedAt:     studio.FormatTime(sample.CreatedAt),
		UpdatedAt:     studio.FormatTime(sample.UpdatedAt),
		Capabilities:  studio.SampleCapabilities{CanViewHistory: true},
	}
}

// toSampleSummaryWithReview 转换列表项（列表已在同一次查询里带出投影）。
func toSampleSummaryWithReview(item store.SampleWithReview) sampleSummary {
	summary := toSampleSummary(item.Sample)
	summary.LatestVersionID = item.LatestVersionID
	summary.ReviewStatus = item.ReviewStatus
	summary.AggregateReviewRevision = item.AggregateReviewRevision
	summary.ReviewConflict = item.ReviewConflict
	return summary
}

// applyReviewProjection 把审阅投影写进摘要。
func applyReviewProjection(summary *sampleSummary, projection model.ReviewProjection) {
	summary.ReviewStatus = projection.EffectiveAction
	summary.AggregateReviewRevision = projection.AggregateReviewRevision
	summary.ReviewConflict = projection.Conflict
}

// listSamples 列出项目的样本。
//
// 尚未支持的筛选（status/risk）在这里**显式拒绝**而不是忽略：契约 §3 列出了
// 它们，但其判据来自人工判断与证据（T16/T17 才建表）。静默忽略会让用户
// 以为自己看到的是筛过的结果 —— 而「列表看起来正常」让这种错误完全不可见。
func (app *application) listSamples(w http.ResponseWriter, r *http.Request) {
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

	query, err := studio.ParseListQuery(r.URL.Query(), 20)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// 审阅状态筛选（T17 接入；T08 曾在此显式拒绝并点名负责的任务）。
	// 取值是**投影**的有效处置，因此「待审阅」包含从未被判断过的内容
	//（它们没有投影行，按 pending 处理）。
	// 显式指定任何一个审阅状态（含 accepted）时不再套用「只看未审阅」，
	// 否则「筛选已接纳」会永远得到空列表 —— 而那会让人以为判断丢了。
	// `status=all` is an explicit full-range request used by quality/release
	// planning. It must not be confused with an omitted status: omitted means
	// the review queue's default pending-only view, while `all` means include
	// every effective review state. Normalize it before passing the query to
	// the store so the SQL remains a single, auditable predicate.
	reviewStatus, reviewedExplicitly, valid := normalizeSampleReviewStatus(query.Status)
	if !valid {
		app.writeStudioError(w, r, studio.NewValidationError(
			"审阅状态筛选只能是 pending、accepted、quarantined 或 conflict",
			[]model.FieldError{{Field: "status",
				Message: "只能按有效处置筛选（pending/accepted/quarantined/conflict）"}}))
		return
	}
	if query.Risk != "" {
		// 风险筛选需要规则命中证据的聚合，属于 T15 的证据面；
		// 这里仍显式拒绝而不是忽略 —— 静默忽略会让用户以为看到的是筛过的结果。
		app.writeStudioError(w, r, studio.NewValidationError(
			"按风险筛选尚未接入（依赖规则命中证据的聚合），请先按审阅状态或关键词筛选",
			[]model.FieldError{{Field: "risk",
				Message: "风险筛选尚未接入：它需要按规则命中证据聚合"}}))
		return
	}

	var batchID int64
	if query.Batch != "" {
		parsed, err := studio.ParseBatchID(query.Batch)
		if err != nil {
			app.writeStudioError(w, r, err)
			return
		}
		// 批次必须属于本项目：否则 `?batch=` 会变成一个跨项目的存在性探测。
		batch, err := app.studio.Batches.GetBatch(r.Context(), parsed)
		if err != nil || batch.ProjectID != projectID {
			app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound,
				"未找到该批次，请返回批次列表刷新后重试"))
			return
		}
		batchID = parsed
	}

	samples, err := app.studio.Batches.ListSamples(r.Context(), store.SampleListQuery{
		ProjectID: projectID,
		BatchID:   batchID,
		Search:    query.Search,
		// 默认只看待审阅（`/review` 的默认队列口径）：已接纳的内容不该
		// 占据审阅者的第一屏，那会让真正的待办被淹没。
		UnreviewedOnly: !reviewedExplicitly,
		ReviewStatus:   reviewStatus,
		Cursor:         query.Cursor.Time,
		CursorID:       query.Cursor.ID,
		Limit:          query.Limit + 1,
	})
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	summaries := make([]sampleSummary, 0, len(samples))
	for _, sample := range samples {
		summary := toSampleSummaryWithReview(sample)
		summary.Capabilities.CanReview = decision.Role == model.ProjectRoleOwner || decision.Role == model.ProjectRoleReviewer
		summaries = append(summaries, summary)
	}
	app.writeJSON(w, http.StatusOK, studio.NewPage(summaries, query.Limit, "createdAt:desc",
		func(summary sampleSummary) studio.Cursor {
			return studio.Cursor{Time: parseAPITime(summary.CreatedAt), ID: summary.SampleID}
		}))
}

// normalizeSampleReviewStatus keeps the two intentional meanings of an empty
// status separate: an omitted value selects the review queue default, while
// an explicit `all` asks for the complete sample set. The store receives an
// empty ReviewStatus for both because `UnreviewedOnly` carries that distinction.
func normalizeSampleReviewStatus(raw string) (status string, explicit bool, valid bool) {
	switch raw {
	case "":
		return "", false, true
	case "all":
		return "", true, true
	case model.EffectivePending, model.EffectiveAccepted, model.EffectiveQuarantined, model.EffectiveConflict:
		return raw, true, true
	default:
		return "", false, false
	}
}

// ---------------------------------------------------------------------------
// GET P/samples/{sampleId}（契约 §3 的 D02）
// ---------------------------------------------------------------------------

// sampleVersionView 是样本版本的内容视图。
//
// Payload 原样透出：它是**不可变内容**，服务端不加工（§4.1「原始内容永不覆盖」）。
// 加工过再返回会让「界面看到的内容」与「发布文件里的内容」不是同一份。
type sampleVersionView struct {
	VersionID     int64  `json:"versionId"`
	SampleID      int64  `json:"sampleId"`
	Version       int    `json:"version"`
	TargetKind    string `json:"targetKind"`
	SchemaVersion string `json:"schemaVersion"`
	ContentHash   string `json:"contentHash"`
	BatchID       *int64 `json:"batchId,omitempty"`
	BatchItemID   *int64 `json:"batchItemId,omitempty"`
	Attempt       int    `json:"attempt"`
	// Source 说明这条内容是哪来的（版本来源单独页要能回答「谁生成的」）。
	Source struct {
		BatchID              *int64 `json:"batchId,omitempty"`
		StandardVersionID    *int64 `json:"standardVersionId,omitempty"`
		StandardContentHash  string `json:"standardContentHash"`
		BlueprintVersionID   *int64 `json:"blueprintVersionId,omitempty"`
		BlueprintContentHash string `json:"blueprintContentHash"`
	} `json:"source"`
	CreatedBy *int64 `json:"createdBy,omitempty"`
	CreatedAt string `json:"createdAt"`
	Payload   any    `json:"payload"`
}

// toSampleVersionView 转换样本版本。
//
// ContentHash 必须带上：判断与发布都用它做竞争检测（T20 的
// aggregate_review_revision 与内容 hash 配套），而界面要能显示
// 「你正在看的是哪一版内容」。
func toSampleVersionView(version model.SampleVersion) sampleVersionView {
	view := sampleVersionView{
		VersionID:     version.ID,
		SampleID:      version.SampleID,
		Version:       version.Version,
		TargetKind:    version.TargetKind,
		SchemaVersion: version.SchemaVersion,
		ContentHash:   version.ContentHash,
		BatchID:       version.BatchID,
		BatchItemID:   version.BatchItemID,
		Attempt:       version.Attempt,
		CreatedBy:     version.CreatedBy,
		CreatedAt:     studio.FormatTime(version.CreatedAt),
		Payload:       version.Payload,
	}
	view.Source.BatchID = version.BatchID
	view.Source.StandardVersionID = version.StandardVersionID
	view.Source.StandardContentHash = version.StandardContentHash
	view.Source.BlueprintVersionID = version.BlueprintVersionID
	view.Source.BlueprintContentHash = version.BlueprintContentHash
	return view
}

// getSample 读取样本及其最新版本内容。
//
// 内容**只读**：本端点只有 GET，且 sample_versions 没有 UPDATE 路径（T05）。
func (app *application) getSample(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, sample, ok := app.resolveSamplePath(w, r, user.ID)
	if !ok {
		return
	}
	version, err := app.studio.Batches.GetSampleVersion(r.Context(), projectID, sample.ID, sample.LatestVersion)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	capabilities := app.sampleCapabilities(r.Context(), projectID, user.ID)
	envelope := studio.NewEnvelope(
		studio.SampleResourceID(sample.ID), "ready", int64(sample.LatestVersion), sample.UpdatedAt,
		capabilities,
		studio.Links{
			"self":    sampleLinks(projectID, sample.ID)["self"],
			"history": sampleLinks(projectID, sample.ID)["history"],
			"project": projectPrefix + "/" + strconv.FormatInt(projectID, 10),
		},
		nil,
	)
	// 审阅投影必须**真实加载**再补进摘要（T17）：不加载就填默认值会把
	// 「已被接纳」显示成「待判断」，而用户会据此重复审一遍已经看过的东西。
	summary := toSampleSummary(sample)
	summary.LatestVersionID = version.ID
	summary.Capabilities = capabilities
	if projection, err := app.studio.Reviews.GetProjection(r.Context(), projectID, version.ID); err == nil {
		applyReviewProjection(&summary, projection)
	} else {
		// 取不到投影不阻塞详情页（内容本身仍可读），但必须留日志：
		// 静默显示「待判断」会让审阅者重复劳动。
		app.logInternal(r, "load review projection failed", err)
	}

	envelope.Data = struct {
		Sample  sampleSummary     `json:"sample"`
		Version sampleVersionView `json:"version"`
	}{Sample: summary, Version: toSampleVersionView(version)}
	app.writeStudioEnvelope(w, http.StatusOK, envelope)
}

// getSampleVersion 读取指定版本。
func (app *application) getSampleVersion(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, sample, ok := app.resolveSamplePath(w, r, user.ID)
	if !ok {
		return
	}
	versionNumber, err := parseVersionSegment(r.PathValue("version"))
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	version, err := app.studio.Batches.GetSampleVersion(r.Context(), projectID, sample.ID, versionNumber)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	envelope := studio.NewEnvelope(
		studio.SampleResourceID(sample.ID)+"/v"+strconv.Itoa(versionNumber), "ready",
		int64(versionNumber), sample.UpdatedAt,
		app.sampleCapabilities(r.Context(), projectID, user.ID),
		sampleLinks(projectID, sample.ID), nil,
	)
	envelope.Data = toSampleVersionView(version)
	app.writeStudioEnvelope(w, http.StatusOK, envelope)
}

// sampleCapabilities derives the command affordances for a sample from the
// current server-side project role.  The read endpoint has already established
// AuthzRead in resolveSamplePath; this second check is intentionally separate:
// a viewer may read the immutable content, but only an owner/reviewer may
// submit a judgment.  Capabilities are only UI hints, so an unavailable authz
// check fails closed (the write endpoint still performs the authoritative
// check).
func (app *application) sampleCapabilities(ctx context.Context, projectID, userID int64) studio.SampleCapabilities {
	capabilities := studio.SampleCapabilities{CanViewHistory: true}
	if _, err := app.studio.Authorize(ctx, projectID, userID, store.AuthzReview); err == nil {
		capabilities.CanReview = true
	}
	return capabilities
}

// getSampleHistory 是「版本与来源」页的数据（契约 §3 的 D03）。
//
// 返回**全部版本**（不只有最新）：T17 要求这一页能回答「内容是谁生成的、
// 用的是哪一版标准与蓝图」，而版本时间线是它的骨架。
func (app *application) getSampleHistory(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, sample, ok := app.resolveSamplePath(w, r, user.ID)
	if !ok {
		return
	}
	query, err := studio.ParseListQuery(r.URL.Query(), 50)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	versions, err := app.studio.Batches.ListSampleVersions(r.Context(), projectID, sample.ID, query.Limit+1)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	view := make([]sampleVersionView, 0, len(versions))
	for _, version := range versions {
		view = append(view, toSampleVersionView(version))
	}
	app.writeJSON(w, http.StatusOK, studio.NewPage(view, query.Limit, "version:desc",
		func(item sampleVersionView) studio.Cursor {
			return studio.Cursor{ID: item.VersionID}
		}))
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// sampleLinks 是样本的可跳转链接。
func sampleLinks(projectID, sampleID int64) studio.Links {
	base := projectPrefix + "/" + strconv.FormatInt(projectID, 10) + "/samples/" + studio.SampleResourceID(sampleID)
	return studio.Links{
		"self":    base,
		"history": base + "/history",
	}
}

// resolveSamplePath 解析并校验 `P/samples/{sampleId}` 路径。
//
// 与 resolveBatchPath 同一原则：**必须**确认样本属于该项目。
// 样本 ID 是全局自增的，只用它取样本会让跨项目读取成为一个合法请求。
func (app *application) resolveSamplePath(w http.ResponseWriter, r *http.Request, userID int64) (int64, model.Sample, bool) {
	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, msgProjectNotFound))
		return 0, model.Sample{}, false
	}
	if _, err := app.studio.Authorize(r.Context(), projectID, userID, store.AuthzRead); err != nil {
		app.writeStudioError(w, r, err)
		return 0, model.Sample{}, false
	}
	sampleID, err := studio.ParseSampleID(r.PathValue("sampleId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该样本，请返回样本列表刷新后重试"))
		return 0, model.Sample{}, false
	}
	sample, err := app.studio.Batches.GetSample(r.Context(), projectID, sampleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该样本，请返回样本列表刷新后重试"))
			return 0, model.Sample{}, false
		}
		app.writeStudioError(w, r, err)
		return 0, model.Sample{}, false
	}
	return projectID, sample, true
}

// parseVersionSegment 解析 `{version}` 路径段。
//
// 版本号从 1 开始：把 0 或负数当成合法会让 `GetSampleVersion` 去查一个
// 永远不存在的版本并返回「内容还没生成」这种误导性文案。
func parseVersionSegment(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, studio.NewValidationError("版本号必须是正整数",
			[]model.FieldError{{Field: "version", Message: "版本号必须是正整数"}})
	}
	return value, nil
}
