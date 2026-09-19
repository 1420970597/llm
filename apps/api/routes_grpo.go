package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L4 lane 独占。通过路由注册表接入，不修改 main.go。

// grpoApp 保存 main() 在 applyRouteRegistrars 时注入的 application。
//
// datasetRouter 的签名是 (w, r, id, rest)，拿不到 application，而
// RegisterDatasetRouter 又必须在 init() 里调用（此时 app 还不存在）。
// 因此先用 RegisterRoutes 捕获 app，再由 routeGrpo 读取。
// applyRouteRegistrars 在 main() 里、ListenAndServe 之前执行，
// 所以请求到达时 grpoApp 必然已就绪。
var grpoApp *application

func init() {
	// PUT /api/v1/datasets/{id}/reward-levels
	// 顺带捕获 application 供数据集子路由使用。
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		grpoApp = app
		mux.HandleFunc("PUT /api/v1/datasets/", app.routeRewardLevels)
	})

	// 数据集子路由：GET /api/v1/datasets/{id}/grpo
	//               POST /api/v1/datasets/{id}/grpo/generate
	RegisterDatasetRouter("grpo", routeGrpo)
}

func (app *application) grpoStore() *store.GrpoStore {
	return store.NewGrpoStore(app.db())
}

func routeGrpo(w http.ResponseWriter, r *http.Request, id int64, rest string) {
	app := grpoApp
	if app == nil {
		http.Error(w, "application not ready", http.StatusInternalServerError)
		return
	}

	switch {
	case rest == "" && r.Method == http.MethodGet:
		app.listGrpoPrompts(w, r, id)
	case rest == "/generate" && r.Method == http.MethodPost:
		app.enqueueGrpoGeneration(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// listGrpoPrompts 返回该数据集已生成的教师模型评判提示词。
func (app *application) listGrpoPrompts(w http.ResponseWriter, r *http.Request, id int64) {
	items, err := app.grpoStore().ListPrompts(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// enqueueGrpoGeneration 设置打分档次并触发 GRPO 提示词生成。
//
// 打分档次若在请求体中给出则先落库，再入队；worker 从 datasets.reward_levels 读取，
// 因此不需要扩展 jobPayload（那是冻结结构）。
func (app *application) enqueueGrpoGeneration(w http.ResponseWriter, r *http.Request, id int64) {
	var input model.GrpoGenerateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err.Error() != "EOF" {
			app.writeError(w, http.StatusBadRequest, err)
			return
		}
	}

	dataset, err := app.datasets.GetDataset(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusNotFound, newUserFacingError(msgDatasetNotFound, err))
		return
	}

	levels := normalizeRewardLevels(input.Levels)
	if len(levels) == 0 {
		levels = normalizeRewardLevels(dataset.RewardLevels)
	}
	if len(levels) < 2 {
		app.writeError(w, http.StatusBadRequest, errors.New(msgRewardLevelsTooFew))
		return
	}
	if err := app.datasets.UpdateRewardLevels(r.Context(), id, levels); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	questions, err := app.pipeline.ListQuestions(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(questions) == 0 {
		app.writeError(w, http.StatusConflict, errors.New(msgNoQuestions))
		return
	}

	enqueued, err := app.enqueueJob(r.Context(), "grpo.generate", id, "grpo_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		app.audit(r.Context(), "enqueue", "grpo_prompts", datasetIDString(id), "grpo.generate levels="+strings.Join(levels, ","))
	}
	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  id,
		Stage:      "grpo",
		State:      "queued",
		Message:    queuedMessage(enqueued, "GRPO 教师评判提示词生成任务已入队", "GRPO 教师评判提示词生成任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

// routeRewardLevels 处理 PUT /api/v1/datasets/{id}/reward-levels。
func (app *application) routeRewardLevels(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	if !strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/reward-levels") {
		http.NotFound(w, r)
		return
	}

	var input model.RewardLevelsRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	levels := normalizeRewardLevels(input.Levels)
	if len(levels) < 2 {
		app.writeError(w, http.StatusBadRequest, errors.New(msgRewardLevelsTooFew))
		return
	}
	if err := app.datasets.UpdateRewardLevels(r.Context(), id, levels); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.audit(r.Context(), "update", "dataset_reward_levels", datasetIDString(id), strings.Join(levels, ","))
	app.writeJSON(w, http.StatusOK, model.RewardLevelsResponse{Levels: levels})
}

// normalizeRewardLevels 清洗打分档次：去空白、去重，保留用户给定顺序（档次有序）。
func normalizeRewardLevels(levels []string) []string {
	normalized := make([]string, 0, len(levels))
	seen := map[string]struct{}{}
	for _, level := range levels {
		trimmed := strings.TrimSpace(level)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}
