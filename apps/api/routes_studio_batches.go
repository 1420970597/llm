package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 Atelier 批次命令与读模型（Issue #160 T08，契约 §2.3、§2.4、§3）。
//
// 三条属于本文件的关键要求：
//
//  1. **批次与作业同事务**（T06 的原子性要求）：创建命令必须落库批次与
//     job/outbox 一次完成，否则「API 落库后、派发前崩溃」会留下一个
//     用户看到「已排队」却永远不会被执行的批次。
//  2. **幂等键绑定 actor + project + command + 请求摘要**（契约 §1.3）。
//     这里同时用两层幂等：命令层（idempotency_records，保证「重复点击不建两个批次」）
//     与作业层（jobs 的唯一索引，保证「同一次命令不产生两个作业」）。
//  3. **未知子资源 404、非法状态 409**（契约 §6）：前者交给 ServeMux 与
//     显式的存在性检查，后者由 store 的状态机给出，这里只做映射。

func init() {
	RegisterRoutes(registerStudioBatchRoutes)
}

// registerStudioBatchRoutes 注册批次与样本相关路由。
//
// 用 `{batchId}`/`{sampleId}` 通配而不是自己解析路径：Go 1.22+ ServeMux
// 对未注册的路径直接 404，这正好是「未知子资源 404」的实现方式 ——
// 手写 strings.Split 很容易把 `/batches/1/unknown` 当成合法请求处理。
func registerStudioBatchRoutes(mux *http.ServeMux, app *application) {
	mux.HandleFunc("POST "+projectPrefix+"/{projectId}/batches", app.createBatch)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/batches", app.listBatches)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/batches/{batchId}", app.getBatch)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/batches/{batchId}/items", app.listBatchItems)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/batches/{batchId}/events", app.listBatchEvents)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/batches/{batchId}/failures", app.listBatchFailures)
	mux.HandleFunc("POST "+projectPrefix+"/{projectId}/batches/{batchId}/pause", app.controlBatch("pause"))
	mux.HandleFunc("POST "+projectPrefix+"/{projectId}/batches/{batchId}/resume", app.controlBatch("resume"))
	mux.HandleFunc("POST "+projectPrefix+"/{projectId}/batches/{batchId}/retry-failed", app.controlBatch("retry-failed"))

	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/overview", app.projectOverview)

	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/samples", app.listSamples)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/samples/{sampleId}", app.getSample)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/samples/{sampleId}/history", app.getSampleHistory)
	mux.HandleFunc("GET "+projectPrefix+"/{projectId}/samples/{sampleId}/versions/{version}", app.getSampleVersion)
}

// ---------------------------------------------------------------------------
// 错误出口
// ---------------------------------------------------------------------------

// writeStudioError 是 Atelier 端点的**唯一**错误出口。
//
// 放在这里而不是复用 writeAPIEntityError：后者是 T02 为项目命令写的，
// 而契约 §1.2 新增了 429（预算）与 blockers。两处各自演化会让
// 「同一个错误在批次端点是 409、在项目端点是 500」这类不一致长期存在。
func (app *application) writeStudioError(w http.ResponseWriter, r *http.Request, err error) {
	var contractErr *studio.Error
	if errors.As(err, &contractErr) {
		app.writeAPIError(w, r, studio.StatusFor(err), contractErr.Code, contractErr.Message, contractErr.FieldErrors)
		return
	}

	if fieldErrors, ok := model.HasFieldErrors(err); ok {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, studio.CodeValidation, err.Error(), fieldErrors)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		app.writeAPIError(w, r, http.StatusNotFound, studio.CodeNotFound, msgProjectNotFound, nil)
		return
	}
	if errors.Is(err, store.ErrBudgetExhausted) {
		// 契约 §1.2 把预算预留失败定为 429。可重试为 true 是**有意的**：
		// 用户加预算或等结算释放额度后，同一个命令就能成功。
		app.writeAPIError(w, r, http.StatusTooManyRequests, studio.CodeBudget,
			"本项目（或该批次）的预算额度已用完，已阻止新的外部调用；请提高预算上限后重试", nil)
		return
	}
	if errors.Is(err, store.ErrBatchNotControllable) {
		// 状态机拒绝：契约 §1.2 的 409。文案来自 store（它知道自己是哪个状态）。
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeConflict, err.Error(), nil)
		return
	}
	if store.IsStoreValidationError(err) {
		app.writeAPIError(w, r, http.StatusUnprocessableEntity, studio.CodeValidation, err.Error(), nil)
		return
	}
	// 兜底沿用既有脱敏路径：绝不把原始 SQL 或密钥给前端。
	app.writeError(w, http.StatusInternalServerError, err)
}

// writeStudioEnvelope 写一个对象响应。
func (app *application) writeStudioEnvelope(w http.ResponseWriter, status int, envelope studio.Envelope) {
	app.writeJSON(w, status, envelope)
}

// ---------------------------------------------------------------------------
// POST P/batches（契约 §2.3）
// ---------------------------------------------------------------------------

// batchCreateRequest 是启动批次的请求体。
type batchCreateRequest struct {
	Purpose                string `json:"purpose"`
	BlueprintVersionID     int64  `json:"blueprintVersionId"`
	CoverageVersionID      int64  `json:"coverageVersionId"`
	StandardVersionID      int64  `json:"standardVersionId"`
	QualityPolicyVersionID int64  `json:"qualityPolicyVersionId"`
	MappingVersionID       int64  `json:"mappingVersionId"`
	UnitCount              int    `json:"unitCount"`
	Budget                 struct {
		Currency   string `json:"currency"`
		LimitMinor *int64 `json:"limitMinor"`
	} `json:"budget"`
	CoverageSlice json.RawMessage `json:"coverageSlice"`
}

// toModel 转成 store 层的创建输入。
func (request batchCreateRequest) toModel() model.CreateBatchInput {
	return model.CreateBatchInput{
		Purpose:                request.Purpose,
		BlueprintVersionID:     request.BlueprintVersionID,
		CoverageVersionID:      request.CoverageVersionID,
		StandardVersionID:      request.StandardVersionID,
		QualityPolicyVersionID: request.QualityPolicyVersionID,
		MappingVersionID:       request.MappingVersionID,
		UnitCount:              request.UnitCount,
		// Budget 直接赋值：两边的匿名结构逐字段一致（含 tag），
		// 因此是同一个类型 —— 中间再抄一遍只会多一处漏字段的机会。
		Budget:        request.Budget,
		CoverageSlice: request.CoverageSlice,
	}
}

// createBatch 启动一次试制或扩量批次（契约 §2.3）。
//
// 顺序刻意是「授权 → 校验 → 幂等 → 事务」：
// 先授权是为了不把「项目是否存在」泄漏给非成员（校验错误也会泄漏），
// 而幂等判定必须在执行**之前**，否则重复点击会先建一个批次再回放。
func (app *application) createBatch(w http.ResponseWriter, r *http.Request) {
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

	var request batchCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError(
			"请求格式有误，请检查填写的内容后重试", nil))
		return
	}
	input := request.toModel()
	input.Normalize()
	if err := input.Validate(); err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	digest := studio.Digest(request)
	scope := studio.CommandScope("batch.create", projectID)
	key := r.Header.Get("Idempotency-Key")
	guard, err := app.studio.GuardIdempotency(r.Context(), scope, user.ID, key, digest)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	switch guard.Outcome {
	case studio.IdempotencyConflict:
		app.writeStudioError(w, r, studio.NewError(studio.CodeIdempotency,
			"该请求标识已用于另一次不同的启动请求，请勿复用同一个 Idempotency-Key"))
		return
	case studio.IdempotencyReplay:
		batch, err := app.studio.Batches.GetBatch(r.Context(), guard.ResourceID)
		if err != nil {
			app.writeStudioError(w, r, err)
			return
		}
		app.writeStudioEnvelope(w, http.StatusAccepted, app.batchEnvelope(batch, decision.Role))
		return
	}

	// 作业幂等键与命令幂等键同源：都用 actor + project + command + 摘要。
	// 两层都做是**有意的冗余** —— 命令层防止「建两个批次」，作业层防止
	// 「同一批次派发两个作业」，而后者的后果是两个 worker 抢同一批单元。
	jobKey, err := model.JobIdempotencyKey(user.ID, projectID, "batch.create", digest)
	if err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求缺少必要的幂等信息", nil))
		return
	}
	job := &store.EnqueueJobInput{
		Kind:           model.JobKindBatchGenerate,
		IdempotencyKey: jobKey,
		CreatedBy:      &user.ID,
		Payload: map[string]any{
			"purpose": input.Purpose,
			"units":   input.UnitCount,
		},
	}

	batch, createdJob, err := app.studio.Batches.CreateBatchWithJob(r.Context(), projectID, user.ID,
		decision.Project.TargetKind, input, job)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	if err := app.studio.RecordIdempotency(r.Context(), scope, user.ID, key, digest,
		batch.ID, http.StatusAccepted); err != nil {
		// 记录失败不改变「批次已创建」这一事实，但必须留日志：
		// 否则用户重试时会创建第二个批次，而那是会真实花钱的操作。
		app.logInternal(r, "idempotency save failed for batch.create", err)
	}

	envelope := app.batchEnvelope(batch, decision.Role)
	if createdJob != nil {
		envelope.Links["job"] = "/api/v1/projects/" + strconv.FormatInt(projectID, 10) +
			"/batches/" + studio.BatchResourceID(batch.ID)
	}
	app.writeStudioEnvelope(w, http.StatusAccepted, envelope)
}

// ---------------------------------------------------------------------------
// GET P/batches（契约 §3）
// ---------------------------------------------------------------------------

// listBatches 列出批次。
//
// 刻意**不**提供「当前运行批次」：契约 §3 明确「不按最大 ID 猜『当前运行』」。
// 同项目可以有多个并行批次，界面应当把它们都列出来。
func (app *application) listBatches(w http.ResponseWriter, r *http.Request) {
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
	if query.Purpose != "" && query.Purpose != model.BatchPurposePilot && query.Purpose != model.BatchPurposeScale {
		app.writeStudioError(w, r, studio.NewValidationError("用途筛选只能是 pilot 或 scale",
			[]model.FieldError{{Field: "purpose", Message: "只能是 pilot 或 scale"}}))
		return
	}

	// 多取一条用于判断「还有下一页」（契约 §1.5 的 nextCursor）。
	batches, err := app.studio.Batches.ListBatches(r.Context(), store.BatchListQuery{
		ProjectID: projectID,
		Purpose:   query.Purpose,
		Status:    query.Status,
		Cursor:    query.Cursor.Time,
		CursorID:  query.Cursor.ID,
		Limit:     query.Limit + 1,
	})
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	summaries := make([]studio.BatchSummary, 0, len(batches))
	for _, batch := range batches {
		summaries = append(summaries, studio.ToBatchSummaryWithCapabilities(
			batch, studio.BatchCapabilitiesFor(decision.Role, batch.Status),
		))
	}
	app.writeJSON(w, http.StatusOK, studio.NewPage(summaries, query.Limit, "createdAt:desc",
		func(summary studio.BatchSummary) studio.Cursor {
			// 游标用的是**排序键**（createdAt, id），与 SQL 的
			// `ORDER BY created_at DESC, id DESC` 必须一致。
			return studio.Cursor{Time: parseAPITime(summary.CreatedAt), ID: summary.BatchID}
		}))
}

// ---------------------------------------------------------------------------
// GET P/batches/{batchId}（契约 §3）
// ---------------------------------------------------------------------------

// getBatchDetail 是批次详情响应。
type getBatchDetail struct {
	Batch    studio.BatchSummary `json:"batch"`
	Steps    []model.BatchStep   `json:"steps"`
	Snapshot model.BatchSnapshot `json:"snapshot"`
	// GenerationConfig 是**内联快照**：批次必须能解释自己当时是怎么跑的，
	// 而蓝图后续可以被改（§2.6「改模型/标准必须新建批次」）。
	GenerationConfig model.BatchGenerationConfig `json:"generationConfig"`
	// FailureCount 是失败单元数，供界面决定是否显示「异常恢复」入口。
	FailureCount int `json:"failureCount"`
	// BatchBudget 是批次级预算台账（T07）。
	BatchBudget studio.BatchBudgetView `json:"batchBudget"`
	// PendingJobs 是在途作业数（暂停时界面必须显示「在途仍会计费」）。
	PendingJobs int `json:"pendingJobs"`
}

// getBatch 读取批次详情。
func (app *application) getBatch(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectID, batch, ok := app.resolveBatchPath(w, r, user.ID)
	if !ok {
		return
	}

	steps, err := app.studio.Batches.ListBatchSteps(r.Context(), batch.ID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	counts, err := app.studio.Batches.CountBatchItemsByStatus(r.Context(), batch.ID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	jobCounts, err := app.studio.Jobs.CountJobsByStatus(r.Context(), projectID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	_ = counts

	detail := getBatchDetail{
		Batch: studio.ToBatchSummaryWithCapabilities(batch,
			batchCapabilitiesForRole(user.ID, batch, app)),
		Steps:            steps,
		Snapshot:         batch.Snapshot,
		GenerationConfig: batch.GenerationConfig,
		FailureCount:     batch.FailedUnits,
		BatchBudget: studio.BatchBudgetView{
			Currency:       batch.Budget.Currency,
			LimitMinor:     batch.Budget.LimitMinor,
			ReservedMinor:  batch.BudgetReservedMinor,
			SettledMinor:   batch.BudgetSettledMinor,
			UncertainMinor: batch.BudgetUncertainMinor,
		},
		PendingJobs: jobCounts[model.JobStatusPending] + jobCounts[model.JobStatusLeased] +
			jobCounts[model.JobStatusRunning],
	}

	envelope := studio.NewEnvelope(
		studio.BatchResourceID(batch.ID), batch.Status, 0, batch.UpdatedAt,
		batchCapabilitiesForRole(user.ID, batch, app), batchLinks(projectID, batch.ID),
		batchWarnings(batch),
	)
	envelope.Data = detail
	app.writeStudioEnvelope(w, http.StatusOK, envelope)
}

// batchWarnings 给出非阻塞提示。
//
// 暂停中的「在途仍会计费」必须出现：§2.4 要求「暂停只阻止新提交；
// 在途请求仍可完成并计费」，而这句语义只在界面上说一次是不够的 ——
// 用户点完暂停会立刻以为不再花钱。
func batchWarnings(batch model.Batch) []string {
	warnings := []string{}
	if batch.ControlState == model.BatchControlPauseRequested || batch.Status == model.BatchStatusPauseRequested {
		warnings = append(warnings, "已请求暂停：不会提交新请求，但已在途的请求仍会完成并计费")
	}
	if batch.Status == model.BatchStatusPartialFailed {
		warnings = append(warnings, "部分单元失败：成功内容已保留，恢复只会重跑失败与未完成项")
	}
	if batch.Budget.LimitMinor > 0 && batch.BudgetReservedMinor+batch.BudgetSettledMinor+batch.BudgetUncertainMinor >= batch.Budget.LimitMinor {
		warnings = append(warnings, "本批预算已用尽，新的外部调用会被阻止；请提高上限或等待结算")
	}
	return warnings
}

// batchLinks 是批次的可跳转链接（前端不自行拼 URL）。
func batchLinks(projectID, batchID int64) studio.Links {
	base := projectPrefix + "/" + strconv.FormatInt(projectID, 10) + "/batches/" + studio.BatchResourceID(batchID)
	return studio.Links{
		"self":     base,
		"project":  projectPrefix + "/" + strconv.FormatInt(projectID, 10),
		"items":    base + "/items",
		"events":   base + "/events",
		"failures": base + "/failures",
	}
}

// ---------------------------------------------------------------------------
// GET P/batches/{batchId}/items|events|failures
// ---------------------------------------------------------------------------

// listBatchItems 列出批次单元。
//
// 失败项与普通项共用同一端点，用 `status=failed` 区分：单独一条
// `/failures` 路由是 T13 的界面入口，它返回的是同一份数据的视图，
// 两份实现会漂移。
func (app *application) listBatchItems(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	_, batch, ok := app.resolveBatchPath(w, r, user.ID)
	if !ok {
		return
	}
	query, err := studio.ParseListQuery(r.URL.Query(), 20)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	items, err := app.studio.Batches.ListBatchItems(r.Context(), batch.ID, query.Status, query.Limit+1)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	page := studio.NewPage(items, query.Limit, "id:asc", func(item model.BatchItem) studio.Cursor {
		return studio.Cursor{ID: item.ID}
	})
	app.writeJSON(w, http.StatusOK, page)
}

// listBatchFailures 是「异常恢复」页的入口（契约 §3 的 R03）。
func (app *application) listBatchFailures(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	_, batch, ok := app.resolveBatchPath(w, r, user.ID)
	if !ok {
		return
	}
	query, err := studio.ParseListQuery(r.URL.Query(), 20)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	items, err := app.studio.Batches.ListBatchItems(r.Context(), batch.ID, model.ItemStatusFailed, query.Limit+1)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// 每条失败项都带可执行的建议：只显示 `rate_limited` 这类机器码
	// 会让用户不知道下一步做什么（§4.1 的 errorClass 与 ErrorClassAction 配套）。
	view := make([]batchFailureView, 0, len(items))
	for _, item := range items {
		view = append(view, batchFailureView{
			ItemID:          item.ID,
			ItemKey:         item.ItemKey,
			ErrorClass:      item.ErrorClass,
			ErrorMessage:    item.ErrorMessage,
			Retryable:       item.Retryable,
			SuggestedAction: model.ErrorClassAction(item.ErrorClass),
			Attempts:        item.Attempt,
		})
	}
	app.writeJSON(w, http.StatusOK, studio.NewPage(view, query.Limit, "id:asc",
		func(item batchFailureView) studio.Cursor { return studio.Cursor{ID: item.ItemID} }))
}

// batchFailureView 是失败项的展示视图。
type batchFailureView struct {
	ItemID          int64  `json:"itemId"`
	ItemKey         string `json:"itemKey"`
	ErrorClass      string `json:"errorClass"`
	ErrorMessage    string `json:"errorMessage"`
	Retryable       bool   `json:"retryable"`
	SuggestedAction string `json:"suggestedAction"`
	Attempts        int    `json:"attempts"`
}

// listBatchEvents 列出批次事件（§5 的事件契约）。
func (app *application) listBatchEvents(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	_, batch, ok := app.resolveBatchPath(w, r, user.ID)
	if !ok {
		return
	}
	query, err := studio.ParseListQuery(r.URL.Query(), 50)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	events, err := app.studio.Batches.ListBatchEvents(r.Context(), batch.ID, query.Limit+1)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, studio.NewPage(events, query.Limit, "sequence:desc",
		func(event model.BatchEvent) studio.Cursor { return studio.Cursor{ID: int64(event.Sequence)} }))
}

// ---------------------------------------------------------------------------
// POST P/batches/{batchId}/{pause|resume|retry-failed}
// ---------------------------------------------------------------------------

// controlBatch 返回一个批次控制命令的 handler（契约 §2.4）。
//
// 三个动作共用一条路径：它们的授权、路径解析、存在性校验与错误映射完全相同，
// 只有状态机的动作名不同。写三遍会让「其中一个忘了做存在性校验」这种
// 缺陷有发生的空间，而那种缺陷的后果是 404 变成 500。
func (app *application) controlBatch(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requestUser(r)
		if !ok {
			app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
			return
		}
		projectID, batch, ok := app.resolveBatchPath(w, r, user.ID)
		if !ok {
			return
		}
		decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, store.AuthzRun)
		if err != nil {
			app.writeStudioError(w, r, err)
			return
		}

		var updated model.Batch
		switch action {
		case "pause":
			updated, err = app.studio.Batches.PauseBatch(r.Context(), projectID, batch.ID, user.ID)
		case "resume":
			updated, err = app.studio.Batches.ResumeBatch(r.Context(), projectID, batch.ID, user.ID)
		case "retry-failed":
			// 只把可重试的失败项放回 pending，成功内容保留（§2.4）。
			// 返回重置条数而不是「成功」：用户需要知道恢复了多少项，
			// 「点了一下没反应」与「恢复了 0 项」在界面上必须能区分。
			reset, err := app.studio.Batches.MarkRetryableItemsPending(r.Context(), projectID, batch.ID, user.ID)
			if err != nil {
				app.writeStudioError(w, r, err)
				return
			}
			updated, err = app.studio.Batches.GetBatch(r.Context(), batch.ID)
			if err != nil {
				app.writeStudioError(w, r, err)
				return
			}
			envelope := app.batchEnvelopeWithRole(updated, decision.Role)
			envelope.Data = map[string]any{"batch": studio.ToBatchSummaryWithCapabilities(updated,
				studio.BatchCapabilitiesFor(decision.Role, updated.Status)), "resetItems": reset}
			app.writeStudioEnvelope(w, http.StatusOK, envelope)
			return
		default:
			app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未知的批次控制动作"))
			return
		}
		if err != nil {
			app.writeStudioError(w, r, err)
			return
		}

		envelope := app.batchEnvelopeWithRole(updated, decision.Role)
		envelope.Warnings = batchWarnings(updated)
		app.writeStudioEnvelope(w, http.StatusAccepted, envelope)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// resolveBatchPath 解析并校验 `P/batches/{batchId}` 路径。
//
// 三件事合成一处：解析项目 ID、判定读权限、取批次并确认它属于该项目。
// **最后一步是必须的**：批次 ID 是全局自增的，只用它取批次会让
// 「在项目 A 的 URL 下读到项目 B 的批次」成为一个合法请求 —— 那是越权。
func (app *application) resolveBatchPath(w http.ResponseWriter, r *http.Request, userID int64) (int64, model.Batch, bool) {
	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, msgProjectNotFound))
		return 0, model.Batch{}, false
	}
	if _, err := app.studio.Authorize(r.Context(), projectID, userID, store.AuthzRead); err != nil {
		app.writeStudioError(w, r, err)
		return 0, model.Batch{}, false
	}
	batchID, err := studio.ParseBatchID(r.PathValue("batchId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该批次，请返回批次列表刷新后重试"))
		return 0, model.Batch{}, false
	}
	batch, err := app.studio.Batches.GetBatch(r.Context(), batchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该批次，请返回批次列表刷新后重试"))
			return 0, model.Batch{}, false
		}
		app.writeStudioError(w, r, err)
		return 0, model.Batch{}, false
	}
	if batch.ProjectID != projectID {
		// 跨项目引用按「不存在」处理，而不是 403：确认「这个批次存在但不属于你」
		// 会泄漏另一个项目的对象数量与状态。
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该批次，请返回批次列表刷新后重试"))
		return 0, model.Batch{}, false
	}
	return projectID, batch, true
}

// batchEnvelope 组装批次响应（能力位用 owner 之外的默认角色判定见下）。
func (app *application) batchEnvelope(batch model.Batch, role string) studio.Envelope {
	return app.batchEnvelopeWithRole(batch, role)
}

// batchEnvelopeWithRole 组装批次响应。
func (app *application) batchEnvelopeWithRole(batch model.Batch, role string) studio.Envelope {
	envelope := studio.NewEnvelope(
		studio.BatchResourceID(batch.ID), batch.Status, 0, batch.UpdatedAt,
		studio.BatchCapabilitiesFor(role, batch.Status),
		batchLinks(batch.ProjectID, batch.ID), nil,
	)
	envelope.Data = studio.ToBatchSummaryWithCapabilities(batch,
		studio.BatchCapabilitiesFor(role, batch.Status))
	return envelope
}

// batchCapabilitiesForRole 按请求者在项目中的角色派生批次能力位。
func batchCapabilitiesForRole(userID int64, batch model.Batch, app *application) studio.BatchCapabilities {
	role, _, err := app.projects.ProjectRole(contextWithoutCancel(), batch.ProjectID, userID)
	if err != nil {
		// 取不到角色时给**最保守**的能力位：给多了会让用户看到能点但会 403 的按钮，
		// 而那种体验比按钮缺失更让人困惑。
		return studio.BatchCapabilities{}
	}
	return studio.BatchCapabilitiesFor(role, batch.Status)
}

// parseAPITime 解析信封里的时间字符串。
//
// 游标用 time.Time，而信封序列化成字符串（RFC3339Nano），因此这里必须能
// 往返解析。解析失败返回零值 → 游标退化成「只有 ID」，而 SQL 的
// `($5 IS NULL OR ...)` 会把零值当成「没有游标」，导致下一页从第一页开始
// 无限循环。因此解析失败时**必须**报错而不是静默返回零值。
func parseAPITime(raw string) (parsed time.Time) {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return value
}

// contextWithoutCancel 返回一个不随请求取消的上下文。
//
// 用于「响应已经要写了，但仍需要读一次数据库才能算能力位」的场景：
// 请求 ctx 在客户端断开时会被取消，那时能力位查询会失败并退化成
// 「什么都不能做」，于是用户刷新后看到一个只读页面。用
// context.WithoutCancel 保留值（例如租户信息）而去掉取消。
func contextWithoutCancel() context.Context {
	return context.WithoutCancel(context.Background())
}
