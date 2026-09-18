package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 L1「关键词 → n 领域 → m 方向」的 HTTP 层。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.1 节。
//   POST /api/v1/datasets/{id}/directions/generate          202 StageEnqueueResult
//   GET  /api/v1/datasets/{id}/directions                   Domain[]（level=2）
//   GET  /api/v1/datasets/{id}/generation-runs              GenerationRun[]
//   POST /api/v1/datasets/{id}/generation-runs/{stage}/resume 202 StageEnqueueResult

func init() {
	// datasetRouter 签名是 (w, r, id, rest)，不带 app，因此先通过
	// RegisterRoutes 拿到 app，再用闭包注册数据集子路由。
	RegisterRoutes(func(_ *http.ServeMux, app *application) {
		RegisterDatasetRouter("directions", func(w http.ResponseWriter, r *http.Request, id int64, rest string) {
			routeDirections(app, w, r, id, rest)
		})
		RegisterDatasetRouter("generation-runs", func(w http.ResponseWriter, r *http.Request, id int64, rest string) {
			routeGenerationRuns(app, w, r, id, rest)
		})
	})
}

func routeDirections(app *application, w http.ResponseWriter, r *http.Request, id int64, rest string) {
	switch {
	case rest == "" || rest == "/":
		// GET /api/v1/datasets/{id}/directions
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		listDirections(app, w, r, id)
	case rest == "/generate":
		// POST /api/v1/datasets/{id}/directions/generate
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		enqueueDirectionGeneration(app, w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func routeGenerationRuns(app *application, w http.ResponseWriter, r *http.Request, id int64, rest string) {
	switch {
	case rest == "" || rest == "/":
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		listGenerationRuns(app, w, r, id)
	case strings.HasSuffix(rest, "/resume"):
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		stage := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), "/resume")
		resumeGenerationRun(app, w, r, id, stage)
	default:
		http.NotFound(w, r)
	}
}

// enqueueDirectionGeneration 入队方向生成。directionCount 省略时回退 datasets.direction_count。
func enqueueDirectionGeneration(app *application, w http.ResponseWriter, r *http.Request, id int64) {
	var input model.DirectionGenerateRequest
	if r.Body != nil {
		// 请求体可选，空体或非法 JSON 都按「使用数据集默认值」处理。
		_ = json.NewDecoder(r.Body).Decode(&input)
	}

	ctx := r.Context()
	dataset, err := app.datasets.GetDataset(ctx, id)
	if err != nil {
		app.writeError(w, http.StatusNotFound, fmt.Errorf("dataset %d not found", id))
		return
	}

	directionCount := input.DirectionCount
	if directionCount <= 0 {
		directionCount = dataset.DirectionCount
	}
	if directionCount <= 0 {
		directionCount = 3
	}

	domains, err := app.datasets.ListRootDomains(ctx, id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(domains) == 0 {
		app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue directions: dataset %d has no domains, run domains/generate first", id))
		return
	}

	runs := store.NewGenerationRunStore(app.db())

	// 入队语义与续跑一致：若已有可续跑的记录（进行中或上次失败/部分失败），
	// 复用它并保留游标里的断点进度；只有确实没有时才新建。
	// 若用 StartRun（只认 pending/running），partial_failed 后再点生成会新建
	// 一条空游标记录，已完成领域全部丢失。
	run, err := runs.ResumeTarget(ctx, id, store.DirectionStage)
	if err == nil {
		if err := runs.ResumeRun(ctx, run.ID); err != nil {
			app.writeError(w, http.StatusInternalServerError, err)
			return
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		run, err = runs.StartRun(ctx, id, store.DirectionStage, len(domains)*directionCount)
		if err != nil {
			app.writeError(w, http.StatusInternalServerError, err)
			return
		}
	} else {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 只在游标为空时初始化 m 值，保留已有断点进度。
	cursor, err := store.DecodeDirectionCursor(run.Cursor)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if store.ShouldInitCursor(cursor) {
		cursor.DirectionCount = directionCount
		if err := runs.SaveCursor(ctx, run.ID, store.EncodeDirectionCursor(cursor),
			run.DoneUnits, len(domains)*directionCount); err != nil {
			app.writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	enqueued, err := app.enqueueJob(ctx, "directions.generate", id, "directions_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		_ = app.store.WriteAuditLog(ctx, "user", "enqueue", "direction_generation", strconv.FormatInt(id, 10), "directions.generate")
	}

	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  id,
		Stage:      "directions",
		State:      "queued",
		Message:    queuedMessage(enqueued, "方向生成任务已入队", "方向生成任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

// listDirections 返回 level=2 的方向，parentId 指向所属领域。
func listDirections(app *application, w http.ResponseWriter, r *http.Request, id int64) {
	items, err := app.datasets.ListDirections(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func listGenerationRuns(app *application, w http.ResponseWriter, r *http.Request, id int64) {
	runs := store.NewGenerationRunStore(app.db())
	items, err := runs.ListRuns(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// resumeGenerationRun 从断点续跑指定阶段。
// 复用的是同一条 generation_runs 记录，游标里的已完成领域会被 worker 跳过。
func resumeGenerationRun(app *application, w http.ResponseWriter, r *http.Request, id int64, stage string) {
	if stage == "" {
		app.writeError(w, http.StatusBadRequest, fmt.Errorf("stage is required"))
		return
	}
	if !store.IsResumableStage(stage) {
		app.writeError(w, http.StatusBadRequest, fmt.Errorf("stage %q is not resumable", stage))
		return
	}

	ctx := r.Context()
	runs := store.NewGenerationRunStore(app.db())

	run, err := runs.ResumeTarget(ctx, id, stage)
	if err != nil {
		app.writeError(w, http.StatusNotFound, fmt.Errorf("no resumable run for dataset %d stage %s", id, stage))
		return
	}
	// 断点续跑复用同一条运行记录，并累加 attempts 留痕。
	if err := runs.ResumeRun(ctx, run.ID); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 续跑必须绕开 enqueueJob 的 dedup key：若上一次运行的 key 尚未过期，
	// 直接复用会把续跑请求静默丢弃。这里先清掉该 key 再入队。
	jobType := store.JobTypeForStage(stage)
	if err := app.redis.Del(ctx, fmt.Sprintf("dedup:%s:%d", jobType, id)).Err(); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	enqueued, err := app.enqueueJob(ctx, jobType, id, stage+"_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		_ = app.store.WriteAuditLog(ctx, "user", "resume", "generation_run", strconv.FormatInt(run.ID, 10), stage)
	}

	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  id,
		Stage:      stage,
		State:      "queued",
		Message:    queuedMessage(enqueued, "续跑任务已入队", "续跑任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}
