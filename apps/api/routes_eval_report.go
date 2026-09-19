package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/eval"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件实现 L10「多 LLM 汇总统计、分析、可视化结论」的 HTTP 层。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.10 节。
//   GET /api/v1/eval/runs/{id}/report   EvalReport
//   GET /api/v1/eval/runs/{id}/scores   EvalItemScore[]
//
// 路由注册说明：只注册这两个**最具体**的 pattern，不注册 /api/v1/eval/runs/
// 泛前缀 —— 那个前缀属于 L9（创建/列表/items）。Go 1.22+ ServeMux 在两条
// lane 的 pattern 同时注册时，越具体的 pattern 优先匹配；若两条 lane 都注册
// 泛前缀才会 panic。把注册面收到最窄是最安全的做法。
//
// 命名注意：apps/api 是单一 main 包，helper 一律带 evalReport 前缀。
// 同包已被占用的名字包括 L2 的 pathInt64、L12 的 cleaningPathInt64。

func init() {
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		mux.HandleFunc("GET /api/v1/eval/runs/{id}/report", app.evalRunReport)
		mux.HandleFunc("GET /api/v1/eval/runs/{id}/scores", app.evalRunScores)
	})
}

// evalSummaries 构造本 lane 的存储层。
func (app *application) evalSummaries() *store.EvalSummaryStore {
	return store.NewEvalSummaryStore(app.db())
}

// evalReportPathInt64 解析路径中的整数 id。
func evalReportPathInt64(r *http.Request, name string) (int64, error) {
	value, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("路径里的 " + name + " 不是有效的整数")
	}
	return value, nil
}

// evalRunReport 返回某次评估的报告：多 LLM 汇总统计 + 中文分析结论。
//
// 诚实性要求：run 未完成时**不伪造一份全零的「已完成」报告**。
// 此时只返回真实拿到的部分（run 元信息 + 已评分数），并在结论里明确写出
// 当前状态与进度，让用户知道这份报告还不能用来下判断。
func (app *application) evalRunReport(w http.ResponseWriter, r *http.Request) {
	runID, err := evalReportPathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	summaries := app.evalSummaries()

	run, err := summaries.GetRun(ctx, runID)
	if err != nil {
		if store.IsEvalSummaryRunNotFound(err) {
			app.writeError(w, http.StatusNotFound, errors.New(msgEvalRunNotFound))
			return
		}
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	datasetName, err := summaries.DatasetName(ctx, run.DatasetID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	scores, err := summaries.LoadRunScores(ctx, runID, 0, "")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, err := summaries.LoadRunItems(ctx, runID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	judges, err := summaries.LoadRunJudges(ctx, runID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 维度定义从 L8 的 store 取（复用，不重复实现查询）。
	dimensions, err := store.NewEvalDimensionStore(app.db()).ListByKeys(ctx, run.DimensionKeys)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	report, notes := eval.Aggregate(eval.AggregateInput{
		Run:         run,
		DatasetName: datasetName,
		Items:       items,
		Scores:      scores,
		Judges:      judges,
		Dimensions:  dimensions,
	})
	report.Conclusions = eval.BuildConclusions(report, notes, run.Status)
	report.GeneratedAt = time.Now().UTC()

	// 只对已完成的评估落汇总行：未完成的统计量会随进度变化，
	// 落库后会被误当成该 run 的最终结论。
	if run.Status == "completed" {
		if err := summaries.UpsertSummaries(ctx, runID, eval.BuildSummaries(report)); err != nil {
			app.writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	app.writeJSON(w, http.StatusOK, report)
}

// evalRunScores 返回逐条打分明细，支持按裁判与维度过滤。
//
// 两个过滤参数都可省略。非法数字参数返回 400 而不是静默忽略 ——
// 静默忽略会让用户以为过滤生效了，却拿到全量数据得出错误结论。
func (app *application) evalRunScores(w http.ResponseWriter, r *http.Request) {
	runID, err := evalReportPathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	var judgeProviderID int64
	if raw := strings.TrimSpace(r.URL.Query().Get("judgeProviderId")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			app.writeError(w, http.StatusBadRequest,
				errors.New("judgeProviderId 不是有效的非负整数"))
			return
		}
		judgeProviderID = parsed
	}
	dimensionKey := strings.TrimSpace(r.URL.Query().Get("dimensionKey"))

	ctx := r.Context()
	summaries := app.evalSummaries()

	// 先确认 run 存在，否则不存在的 run 会返回空数组而不是 404，
	// 用户无法区分「这个 run 没有分数」与「这个 run 根本不存在」。
	if _, err := summaries.GetRun(ctx, runID); err != nil {
		if store.IsEvalSummaryRunNotFound(err) {
			app.writeError(w, http.StatusNotFound, errors.New(msgEvalRunNotFound))
			return
		}
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	scores, err := summaries.LoadRunScores(ctx, runID, judgeProviderID, dimensionKey)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if scores == nil {
		scores = []model.EvalItemScore{}
	}
	app.writeJSON(w, http.StatusOK, scores)
}
