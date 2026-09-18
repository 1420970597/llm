package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/eval"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L9 lane 独占：评估运行的 HTTP 层。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.9 节。
//
//	POST /api/v1/eval/runs              EvalRunCreateRequest -> EvalRun
//	GET  /api/v1/eval/runs?datasetId=   -> EvalRun[]
//	GET  /api/v1/eval/runs/{id}         -> EvalRunDetail
//	POST /api/v1/eval/runs/{id}/start   202 StageEnqueueResult
//	GET  /api/v1/eval/runs/{id}/items?limit=&offset=  -> EvalItem[]
//
// 路由注册注意：/api/v1/eval/dimensions（L8）与 /api/v1/admin/eval/judges、
// /api/v1/eval/runs/{runId}/judges（L7）已被占用。本文件只注册 /api/v1/eval/runs
// 下的路径，不碰它们的 pattern。
//
// 命名注意：helper 刻意命名为 evalPathInt64 而非通用的 pathInt64，
// 因为 apps/api 是单一 main 包，其他 lane 可能已在同包声明 pathInt64
// （L2 的 routes_chain_standards.go 已占用该名字），重名会导致合并后编译失败。

const evalJobType = "eval.run"

func init() {
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		mux.HandleFunc("POST /api/v1/eval/runs", app.createEvalRun)
		mux.HandleFunc("GET /api/v1/eval/runs", app.listEvalRuns)
		mux.HandleFunc("GET /api/v1/eval/runs/{id}", app.getEvalRun)
		mux.HandleFunc("POST /api/v1/eval/runs/{id}/start", app.startEvalRun)
		mux.HandleFunc("GET /api/v1/eval/runs/{id}/items", app.listEvalRunItems)
	})
}

func (app *application) evalRuns() *store.EvalRunStore {
	return store.NewEvalRunStore(app.db())
}

// evalDimensions 与 evalJudgeStore 已分别由 L8（routes_eval_dimensions.go）与
// L7（routes_eval_judges.go）在同包声明，本文件直接复用，不重复定义——
// apps/api 是单一 main 包，重复定义会让合并后的编译失败。

// evalPathInt64 解析路径参数为 int64。
func evalPathInt64(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.PathValue(name))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid " + name + " in path")
	}
	return value, nil
}

// createEvalRun 创建评估运行（draft 状态），不启动。
//
// 契约刻意把「创建」与「启动」分开：用户可以先把运行建好、配好裁判，
// 再决定何时启动，而不是建即跑。
func (app *application) createEvalRun(w http.ResponseWriter, r *http.Request) {
	var input model.EvalRunCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	if input.DatasetID <= 0 {
		app.writeError(w, http.StatusBadRequest, errors.New("datasetId is required"))
		return
	}

	// 抽样参数在创建时就校验：等到 start 才报错会让用户白配一遍。
	if err := validateSampling(input.SamplingMode, input.SampleRatio, input.SampleSize); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	if _, err := app.datasets.GetDataset(ctx, input.DatasetID); err != nil {
		app.writeError(w, http.StatusNotFound, errors.New("dataset not found"))
		return
	}

	if input.SamplingMode == "" {
		input.SamplingMode = eval.SamplingFull
	}
	if input.TargetKind == "" {
		input.TargetKind = "sft"
	}

	// 生成者 provider 默认取数据集自己的生成模型，供 worker 剔除自评。
	// 用户显式传了就以用户的为准（评估历史数据时可能想换基准）。
	if input.GeneratorProvider == 0 {
		if dataset, err := app.datasets.GetDataset(ctx, input.DatasetID); err == nil {
			input.GeneratorProvider = dataset.ProviderID
		}
	}

	if len(input.DimensionKeys) == 0 {
		// 未指定维度时取全部启用中的维度，而不是留空导致一条也评不了。
		dimensions, err := app.evalDimensions().List(ctx, "", nil)
		if err != nil {
			app.writeError(w, http.StatusInternalServerError, err)
			return
		}
		keys := make([]string, 0, len(dimensions))
		for _, dimension := range dimensions {
			if dimension.IsActive {
				keys = append(keys, dimension.Key)
			}
		}
		input.DimensionKeys = keys
	}

	run, err := app.evalRuns().CreateRun(ctx, input)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	if len(input.JudgeProviderIDs) > 0 {
		// 用户在创建时一并给了裁判，就顺手落库并做剔除判定，
		// 免得多调一次 PUT /eval/runs/{id}/judges。
		if err := app.applyRunJudges(ctx, run.ID, input.JudgeProviderIDs); err != nil {
			app.writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	app.audit(ctx, "create", "eval_run", strconv.FormatInt(run.ID, 10),
		"dataset_id="+strconv.FormatInt(run.DatasetID, 10)+" mode="+run.SamplingMode)

	app.writeJSON(w, http.StatusOK, run)
}

// validateSampling 校验抽样参数。
//
// 复用 internal/eval 的抽样策略做真正的判定，而不是在这里复制一份规则：
// 两处规则一旦分叉，就会出现「接口收下了、worker 却报错」的不一致。
func validateSampling(mode string, ratio float64, size int) error {
	if mode == "" {
		return nil
	}
	// 用一条探针数据触发参数校验分支，避免为了校验而先查库。
	_, err := eval.SampleQuestionIDs(eval.SampleSpec{
		Mode:        mode,
		Ratio:       ratio,
		Size:        size,
		Seed:        1,
		QuestionIDs: []int64{1},
	})
	return err
}

// applyRunJudges 落库裁判选择并返回可用的裁判数量。
//
// 与 L7 的 PUT 接口共用同一套剔除判定（eval.ResolveJudges），
// 保证「创建时指定裁判」与「创建后设置裁判」行为完全一致。
func (app *application) applyRunJudges(ctx context.Context, runID int64, providerIDs []int64) error {
	judgeStore := app.evalJudgeStore()

	generatorProviderID, err := judgeStore.GeneratorProviderID(ctx, runID)
	if err != nil {
		return err
	}

	providers, err := app.store.ListProviders(ctx)
	if err != nil {
		return err
	}

	_, records, err := eval.ResolveJudges(ctx, providers, generatorProviderID)
	if err != nil {
		return err
	}

	wanted := make(map[int64]struct{}, len(providerIDs))
	for _, id := range providerIDs {
		wanted[id] = struct{}{}
	}

	selected := make([]model.EvalRunJudge, 0, len(providerIDs))
	found := make(map[int64]struct{}, len(providerIDs))
	for _, record := range records {
		if _, ok := wanted[record.ProviderID]; !ok {
			continue
		}
		selected = append(selected, record)
		found[record.ProviderID] = struct{}{}
	}

	missing := make([]string, 0)
	for _, id := range providerIDs {
		if _, ok := found[id]; !ok {
			missing = append(missing, strconv.FormatInt(id, 10))
		}
	}
	if len(missing) > 0 {
		return errors.New("unknown provider ids: " + strings.Join(missing, ","))
	}

	_, err = judgeStore.UpsertRunJudges(ctx, runID, selected)
	return err
}

// listEvalRuns 列出评估运行，可按 datasetId 过滤。
func (app *application) listEvalRuns(w http.ResponseWriter, r *http.Request) {
	datasetID := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("datasetId")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			app.writeError(w, http.StatusBadRequest, errors.New("invalid datasetId"))
			return
		}
		datasetID = parsed
	}

	runs, err := app.evalRuns().ListRuns(r.Context(), datasetID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, runs)
}

// getEvalRun 返回运行详情：运行本体 + 裁判（含被剔除项）+ 维度定义。
//
// 返回 EvalRunDetail 而非裸 EvalRun：前端要在一个页面上同时展示
// 「这次评估用了哪些模型/维度」，分三次请求会让页面出现中间态。
func (app *application) getEvalRun(w http.ResponseWriter, r *http.Request) {
	runID, err := evalPathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	runs := app.evalRuns()

	run, err := runs.GetRun(ctx, runID)
	if err != nil {
		if store.IsEvalRunNotFound(err) {
			app.writeError(w, http.StatusNotFound, errors.New("eval run not found"))
			return
		}
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	judges, err := app.evalJudgeStore().ListRunJudges(ctx, runID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	dimensions := []model.EvalDimension{}
	if len(run.DimensionKeys) > 0 {
		dimensions, err = app.evalDimensions().ListByKeys(ctx, run.DimensionKeys)
		if err != nil {
			app.writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	app.writeJSON(w, http.StatusOK, model.EvalRunDetail{
		Run:        run,
		Judges:     judges,
		Dimensions: dimensions,
	})
}

// startEvalRun 启动评估运行（入队），202 + StageEnqueueResult。
func (app *application) startEvalRun(w http.ResponseWriter, r *http.Request) {
	runID, err := evalPathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	runs := app.evalRuns()

	run, err := runs.GetRun(ctx, runID)
	if err != nil {
		if store.IsEvalRunNotFound(err) {
			app.writeError(w, http.StatusNotFound, errors.New("eval run not found"))
			return
		}
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 已在跑的运行不重复入队（入队不会产生第二个任务，只会把状态搅乱）。
	// 注意：completed / failed / draft 都允许重新启动，重跑会覆盖同一批条目的分数
	// （eval_item_scores 按 (条目, 裁判, 维度) upsert），这正是「重跑一次」需要的语义。
	if run.Status == "running" || run.Status == "queued" {
		app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
			DatasetID:  run.DatasetID,
			Stage:      "eval",
			State:      "queued",
			Message:    "评估任务已在队列中",
			AcceptedAt: time.Now().Format(time.RFC3339),
		})
		return
	}

	if len(run.DimensionKeys) == 0 {
		app.writeError(w, http.StatusBadRequest, errors.New("该评估运行没有可用维度，请先配置维度"))
		return
	}

	usableJudges := 0
	judges, err := app.evalJudgeStore().ListRunJudges(ctx, runID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, judge := range judges {
		if !judge.Excluded {
			usableJudges++
		}
	}
	if usableJudges == 0 {
		// 一个可用裁判都没有就入队，worker 只会白跑一趟。
		// 常见原因：只配了一个 provider，而它正是生成者，被自评剔除规则拦下。
		app.writeError(w, http.StatusBadRequest,
			errors.New("没有可用的裁判模型：生成者模型禁止自评，请先配置其他 provider"))
		return
	}

	// 先把运行标为 queued 再入队：worker 拿到 job 时靠 ActiveRun 反查
	// status IN ('queued','running') 的运行（job payload 只带 datasetId），
	// 顺序反了会出现 worker 先到、查不到运行而静默空跑。
	if err := runs.UpdateRunStatus(ctx, runID, "queued", run.TotalItems, run.ScoredItems, ""); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	enqueued, err := app.enqueueJob(ctx, evalJobType, run.DatasetID, "")
	if err != nil {
		// 入队本身失败（Redis 不可用）：必须把状态退回 failed，
		// 否则运行会永远停在 queued，后续 start 只会返回「已在队列中」而永远没人执行。
		_ = runs.UpdateRunStatus(ctx, runID, "failed", run.TotalItems, run.ScoredItems,
			"入队失败："+err.Error())
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !enqueued {
		// 去重键仍在有效期内（上一次运行完成但 TTL 未过）。任务不会被执行，
		// 必须把状态改回可重试的 failed，否则运行会永远停在 queued。
		_ = runs.UpdateRunStatus(ctx, runID, "failed", run.TotalItems, run.ScoredItems,
			"重复入队：去重键仍在有效期内，本次未执行")
	}

	app.audit(ctx, "start", "eval_run", strconv.FormatInt(runID, 10),
		"dataset_id="+strconv.FormatInt(run.DatasetID, 10))

	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  run.DatasetID,
		Stage:      "eval",
		State:      "queued",
		Message:    queuedMessage(enqueued, "评估任务已入队", "评估任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

// listEvalRunItems 列出运行的被评条目（分页）。
func (app *application) listEvalRunItems(w http.ResponseWriter, r *http.Request) {
	runID, err := evalPathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	limit := 0
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			app.writeError(w, http.StatusBadRequest, errors.New("invalid limit"))
			return
		}
		limit = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			app.writeError(w, http.StatusBadRequest, errors.New("invalid offset"))
			return
		}
		offset = parsed
	}

	ctx := r.Context()
	runs := app.evalRuns()

	// 先确认运行存在：否则对不存在的 id 会返回空数组，
	// 前端无法区分「运行不存在」与「运行还没有条目」。
	if _, err := runs.GetRun(ctx, runID); err != nil {
		if store.IsEvalRunNotFound(err) {
			app.writeError(w, http.StatusNotFound, errors.New("eval run not found"))
			return
		}
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	items, err := runs.ListItems(ctx, runID, limit, offset)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}
