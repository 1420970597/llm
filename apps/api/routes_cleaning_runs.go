package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/cleaning"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件实现 L12「分步清洗 + 清洗报告」的 HTTP 层。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.12 节。
//   POST /api/v1/datasets/{id}/cleaning/run         202 StageEnqueueResult
//   GET  /api/v1/datasets/{id}/cleaning/runs        CleaningRun[]
//   GET  /api/v1/cleaning/runs/{id}/report          CleaningReport
//   GET  /api/v1/cleaning/runs/{id}/findings        CleaningFinding[]
//
// 路由注册说明：数据集子路由段 "cleaning" 走 RegisterDatasetRouter（签名
// (w,r,id,rest) 拿不到 app），因此先用 RegisterRoutes 拿到 app 再闭包注册；
// 顶层的 /api/v1/cleaning/runs/{id}/... 是 main.go 未注册过的新前缀，直接
// 用显式 pattern 注册，由 ServeMux 做最长匹配。
//
// 命名注意：helper 刻意命名为 cleaningPathInt64 而非通用的 pathInt64，
// 因为 apps/api 是单一 main 包，其他 lane 可能已在同包声明 pathInt64
// （L2 的 routes_chain_standards.go 已占用该名字），重名会导致合并后编译失败。

const cleaningJobType = "cleaning.run"

func init() {
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		RegisterDatasetRouter("cleaning", func(w http.ResponseWriter, r *http.Request, id int64, rest string) {
			routeDatasetCleaning(app, w, r, id, rest)
		})
		mux.HandleFunc("GET /api/v1/cleaning/runs/{id}/report", app.cleaningRunReport)
		mux.HandleFunc("GET /api/v1/cleaning/runs/{id}/findings", app.cleaningRunFindings)
	})
}

func (app *application) cleaningRuns() *store.CleaningRunStore {
	return store.NewCleaningRunStore(app.db())
}

func cleaningPathInt64(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid " + name + " in path")
	}
	return value, nil
}

// routeDatasetCleaning 处理 /api/v1/datasets/{id}/cleaning/{rest}。
func routeDatasetCleaning(app *application, w http.ResponseWriter, r *http.Request, id int64, rest string) {
	switch strings.TrimSuffix(rest, "/") {
	case "":
		http.NotFound(w, r)
	case "/runs":
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		listCleaningRuns(app, w, r, id)
	case "/run":
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		enqueueCleaningRun(app, w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// listCleaningRuns 列出数据集下的清洗运行历史。
func listCleaningRuns(app *application, w http.ResponseWriter, r *http.Request, datasetID int64) {
	items, err := app.cleaningRuns().ListByDataset(r.Context(), datasetID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// enqueueCleaningRun 入队一次分步清洗。
//
// 幂等语义：若该数据集已有 queued/running 的清洗运行，直接复用并返回
// 「已在队列中」，不新建记录也不重复入队——避免同一数据集被并发清洗两次。
func enqueueCleaningRun(app *application, w http.ResponseWriter, r *http.Request, datasetID int64) {
	var input model.CleaningRunRequest
	if r.Body != nil {
		// 请求体可选：空体或非法 JSON 都按「清洗全部三个阶段」处理。
		_ = json.NewDecoder(r.Body).Decode(&input)
	}

	stages, err := cleaning.NormalizeStages(input.Stages)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	if _, err := app.datasets.GetDataset(ctx, datasetID); err != nil {
		app.writeError(w, http.StatusNotFound, err)
		return
	}

	runs := app.cleaningRuns()
	if _, err := runs.ActiveRun(ctx, datasetID); err == nil {
		app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
			DatasetID:  datasetID,
			Stage:      "cleaning",
			State:      "queued",
			Message:    "清洗任务已在队列中",
			AcceptedAt: time.Now().Format(time.RFC3339),
		})
		return
	} else if !store.IsCleaningRunNotFound(err) {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 先落 run 记录再入队：Create 是同步完成并提交的，worker 消费到任务时
	// 一定能读到这条记录（含用户指定的 stages），不存在竞态。
	run, err := runs.Create(ctx, datasetID, stages)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	enqueued, err := app.enqueueJob(ctx, cleaningJobType, datasetID, "")
	if err != nil {
		_ = runs.MarkFailed(ctx, run.ID, err.Error())
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !enqueued {
		// 去重键仍在有效期内（上一次运行已完成但 TTL 未过）。此时任务不会被执行，
		// 必须把刚建的记录标失败，否则会留下一条永远 queued 的僵尸运行。
		_ = runs.MarkFailed(ctx, run.ID, "重复入队：去重键仍在有效期内，本次未执行")
	}

	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  datasetID,
		Stage:      "cleaning",
		State:      "queued",
		Message:    queuedMessage(enqueued, "清洗任务已入队", "清洗任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

// cleaningRunReport 返回清洗报告（分阶段统计 + Top 关键词 + 结论）。
func (app *application) cleaningRunReport(w http.ResponseWriter, r *http.Request) {
	runID, err := cleaningPathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	runs := app.cleaningRuns()
	run, err := runs.GetRun(r.Context(), runID)
	if err != nil {
		if store.IsCleaningRunNotFound(err) {
			app.writeError(w, http.StatusNotFound, errors.New("cleaning run not found"))
			return
		}
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	report, err := runs.GetReport(r.Context(), runID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(report.Stages) == 0 && report.Run.ID == 0 {
		// 尚未完成，返回骨架 + 明确说明，而不是伪造一份全零的「已完成」报告。
		report = model.CleaningReport{
			Run:         run,
			Stages:      []model.CleaningStageStat{},
			TopKeywords: []model.CleaningKeywordStat{},
			Conclusions: []string{"清洗尚未完成，当前状态：" + run.Status},
			GeneratedAt: time.Now(),
		}
	}
	app.writeJSON(w, http.StatusOK, report)
}

// cleaningRunFindings 返回命中明细，支持按 stage 过滤与 limit 截断。
func (app *application) cleaningRunFindings(w http.ResponseWriter, r *http.Request) {
	runID, err := cleaningPathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	stage := strings.TrimSpace(r.URL.Query().Get("stage"))
	if stage != "" && !cleaning.ValidStage(stage) {
		app.writeError(w, http.StatusBadRequest, errors.New("unknown stage, expected question/reasoning/answer"))
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	items, err := app.cleaningRuns().ListFindings(r.Context(), runID, stage, limit)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}
