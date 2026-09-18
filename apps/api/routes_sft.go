package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L5 lane 独占。通过路由注册表接入，不修改 main.go。

// sftApp 保存 main() 在 applyRouteRegistrars 时注入的 application。
//
// datasetRouter 的签名是 (w, r, id, rest)，拿不到 application，而
// RegisterDatasetRouter 又必须在 init() 里调用（此时 app 还不存在）。
// 因此先用 RegisterRoutes 捕获 app，再由 routeSft 读取。
// applyRouteRegistrars 在 main() 里、ListenAndServe 之前执行，
// 所以请求到达时 sftApp 必然已就绪。
var sftApp *application

func init() {
	// 捕获 application 供数据集子路由使用。
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		sftApp = app
	})

	// 数据集子路由：GET  /api/v1/datasets/{id}/sft
	//               POST /api/v1/datasets/{id}/sft/generate
	RegisterDatasetRouter("sft", routeSft)
}

const sftJobType = "sft.generate"

func (app *application) sftStore() *store.SftStore {
	return store.NewSftStore(app.db())
}

func routeSft(w http.ResponseWriter, r *http.Request, id int64, rest string) {
	app := sftApp
	if app == nil {
		http.Error(w, "application not ready", http.StatusInternalServerError)
		return
	}

	switch {
	case rest == "" && r.Method == http.MethodGet:
		app.listSftRecords(w, r, id)
	case rest == "/generate" && r.Method == http.MethodPost:
		app.enqueueSftGeneration(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// listSftRecords 返回该数据集已生成的 SFT 样本。
func (app *application) listSftRecords(w http.ResponseWriter, r *http.Request, id int64) {
	items, err := app.sftStore().ListRecords(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// enqueueSftGeneration 触发 SFT 样本生成。
//
// 入队载荷只有 {type, datasetId}（enqueueJob 是冻结的共享实现），因此
// 请求体里的 includeAnswer 会落到 generation_runs.cursor，由 worker 读取，
// 这样既不重复实现入队逻辑，也能让「是否生成答案」真实生效。
func (app *application) enqueueSftGeneration(w http.ResponseWriter, r *http.Request, id int64) {
	var input model.SftGenerateRequest
	if r.Body != nil {
		// 允许空请求体：语义为「生成思维链与答案」。
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err.Error() != "EOF" {
			app.writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	dataset, err := app.datasets.GetDataset(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusNotFound, err)
		return
	}
	if dataset.ProviderID <= 0 {
		app.writeError(w, http.StatusConflict, fmt.Errorf("dataset %d has no provider configured", id))
		return
	}

	questions, err := app.pipeline.ListQuestions(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(questions) == 0 {
		app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue sft generation: dataset %d has no questions", id))
		return
	}

	if err := app.saveSftCursor(r.Context(), id, len(questions), input.IncludeAnswer); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	enqueued, err := app.enqueueJob(r.Context(), sftJobType, id, "sft_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		app.audit(r.Context(), "enqueue", "sft_records", datasetIDString(id),
			fmt.Sprintf("sft.generate includeAnswer=%t questions=%d", input.IncludeAnswer, len(questions)))
	}
	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  id,
		Stage:      "sft",
		State:      "queued",
		Message:    queuedMessage(enqueued, "SFT 思维链与答案生成任务已入队", "SFT 思维链与答案生成任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

// saveSftCursor 在入队前把本次任务的参数写入 generation_runs.cursor。
// 复用 GenerationRunStore 的 StartRun + SaveCursor，不另建队列表。
func (app *application) saveSftCursor(ctx context.Context, datasetID int64, totalUnits int, includeAnswer bool) error {
	run, err := app.generationRuns.StartRun(ctx, datasetID, sftJobType, totalUnits)
	if err != nil {
		return err
	}
	return app.generationRuns.SaveCursor(ctx, run.ID, map[string]any{
		"includeAnswer": includeAnswer,
	}, 0, totalUnits)
}
