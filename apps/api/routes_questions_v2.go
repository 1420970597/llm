package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// defaultQuestionsPerDirection 是所有回退都落空时的兜底数量。
const defaultQuestionsPerDirection = store.DefaultQuestionsPerDirection

// questionsStage 是问题生成在 generation_runs 中使用的阶段名。
const questionsStage = store.QuestionsStage

// questionsCursorKey 是难度配比在 generation_runs.cursor 中的键名。
//
// 为什么把 difficultyMix 放在 cursor 里：worker 的 job payload 只有
// {type, datasetId}（见 http_util.go 的 enqueueJob，属冻结契约），
// 配比无法随任务传递。cursor 是既有基础设施中唯一能持久化「本阶段参数」
// 且 worker 可读的位置。
const questionsCursorKey = store.QuestionsCursorMixKey

// questionsCursorPerDirectionKey 是 x 在 cursor 中的键名（便于排查与断点续跑）。
const questionsCursorPerDirectionKey = store.QuestionsCursorPerDirectionKey

// questionsAPI 持有 application 引用。
//
// datasetRouter 的签名是 func(w, r, id, rest)，不含 app（冻结契约），
// 而 RegisterRoutes 的 registrar 能拿到 app。这里用 init() 里的
// RegisterRoutes 捕获 app 存入本变量，供 dataset 子路由使用。
var questionsAPI *application

func init() {
	RegisterRoutes(func(_ *http.ServeMux, app *application) {
		questionsAPI = app
	})

	// 接管 questions 段。注册表优先级高于 main.go 的 legacy 分支
	// （routeDatasetGet / routeDatasetActions 都先调 tryDatasetRouter），
	// 因此这里必须同时保留 legacy 的列表与入队语义。
	RegisterDatasetRouter("questions", routeQuestionsV2)
}

// routeQuestionsV2 处理 /api/v1/datasets/{id}/questions[/rest]。
func routeQuestionsV2(w http.ResponseWriter, r *http.Request, id int64, rest string) {
	app := questionsAPI
	if app == nil {
		app.writeError(w, http.StatusInternalServerError, fmt.Errorf("application not initialized"))
		return
	}

	switch {
	case rest == "" && r.Method == http.MethodGet:
		listQuestionsV2(w, r, app, id)
	case rest == "/difficulty-stats" && r.Method == http.MethodGet:
		questionDifficultyStats(w, r, app, id)
	case rest == "/generate" && r.Method == http.MethodPost:
		enqueueQuestionsV2(w, r, app, id)
	default:
		http.NotFound(w, r)
	}
}

// listQuestionsV2 返回数据集下的问题列表（含 difficulty 与方向名）。
func listQuestionsV2(w http.ResponseWriter, r *http.Request, app *application, id int64) {
	questions, err := store.NewQuestionStoreV2(app.db()).ListQuestions(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, questions)
}

// questionDifficultyStats 返回难度分布统计。
func questionDifficultyStats(w http.ResponseWriter, r *http.Request, app *application, id int64) {
	stats, err := store.NewQuestionStoreV2(app.db()).DifficultyStats(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, stats)
}

// enqueueQuestionsV2 校验请求、持久化 x 与难度配比，然后入队问题生成任务。
func enqueueQuestionsV2(w http.ResponseWriter, r *http.Request, app *application, id int64) {
	var payload model.QuestionGenerateRequestV2
	// 请求体允许为空：保持 legacy「无 body 也能入队」的兼容性。
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	dataset, err := app.datasets.GetDataset(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	questionsPerDirection := resolveQuestionsPerDirection(payload.QuestionsPerDirection, dataset)
	difficultyMix := normalizeDifficultyMix(payload.DifficultyMix)

	questionStore := store.NewQuestionStoreV2(app.db())
	directions, err := questionStore.ListDirections(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(directions) == 0 {
		app.writeError(w, http.StatusConflict, fmt.Errorf(
			"cannot enqueue questions: dataset %d has no directions (level=2 domains); generate directions first", id))
		return
	}

	// x 必须持久化：worker 只收到 datasetId，需要从 datasets 读回同一个 x。
	if questionsPerDirection != dataset.QuestionsPerDirect {
		if err := app.datasets.UpdateQuestionsPerDirection(r.Context(), id, questionsPerDirection); err != nil {
			app.writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	totalUnits := len(directions) * questionsPerDirection
	if _, err := app.generationRuns.StartRun(r.Context(), id, questionsStage, totalUnits); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	run, err := app.generationRuns.ActiveRun(r.Context(), id, questionsStage)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	cursor := map[string]any{
		questionsCursorKey:             difficultyMix,
		questionsCursorPerDirectionKey: questionsPerDirection,
	}
	if err := app.generationRuns.SaveCursor(r.Context(), run.ID, cursor, run.DoneUnits, totalUnits); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	enqueued, err := app.enqueueJob(r.Context(), "questions.generate", id, "questions_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		app.audit(r.Context(), "enqueue", "question_generation", strconv.FormatInt(id, 10),
			fmt.Sprintf("questions.generate x=%d directions=%d", questionsPerDirection, len(directions)))
	}

	message := "问题生成任务已在队列中"
	if enqueued {
		message = fmt.Sprintf("问题生成任务已入队：%d 个方向 × %d 个问题", len(directions), questionsPerDirection)
	}
	app.writeJSON(w, http.StatusAccepted, enqueueResult(id, "questions", enqueued, message, message))
}

// resolveQuestionsPerDirection 按优先级解析 x：
//  1. 请求体显式指定
//  2. 数据集自身配置的 questions_per_direction（用户可控的 x）
//  3. 生成策略推算出的 questions_per_domain（legacy 语义回退）
//  4. 兜底默认值
//
// 保证返回值恒为正数。
func resolveQuestionsPerDirection(requested int, dataset model.Dataset) int {
	if requested > 0 {
		return requested
	}
	if dataset.QuestionsPerDirect > 0 {
		return dataset.QuestionsPerDirect
	}
	if dataset.Estimate.QuestionsPerDomain > 0 {
		return dataset.Estimate.QuestionsPerDomain
	}
	return defaultQuestionsPerDirection
}

// normalizeDifficultyMix 清洗用户给定的难度配比。
// 返回 nil 表示未指定，worker 侧会用默认配比。
func normalizeDifficultyMix(mix map[string]float64) map[string]float64 {
	if len(mix) == 0 {
		return nil
	}
	cleaned := map[string]float64{}
	for level, weight := range mix {
		key := strings.ToLower(strings.TrimSpace(level))
		if key != "easy" && key != "medium" && key != "hard" {
			continue
		}
		if weight <= 0 {
			continue
		}
		cleaned[key] = weight
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}
