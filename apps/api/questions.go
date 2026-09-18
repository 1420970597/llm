package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/1420970597/llm/internal/model"
)

func (app *application) enqueueQuestionGeneration(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	domains, err := app.datasets.ListDomains(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(domains) == 0 {
		app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue questions: dataset %d has no domains", id))
		return
	}

	enqueued, err := app.enqueueDatasetJob(r.Context(), "questions.generate", id, "questions_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		_ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "question_generation", datasetIDString(id), "questions.generate")
	}
	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  id,
		Stage:      "questions",
		State:      "queued",
		Message:    queuedMessage(enqueued, "问题生成任务已入队", "问题生成任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

// enqueueDatasetJob 保留原有签名，内部委托给共享的 enqueueJob 实现。
func (app *application) enqueueDatasetJob(ctx context.Context, jobType string, datasetID int64, queuedStatus string) (bool, error) {
	return app.enqueueJob(ctx, jobType, datasetID, queuedStatus)
}

func queuedMessage(enqueued bool, created string, existing string) string {
	if enqueued {
		return created
	}
	return existing
}

func (app *application) listQuestions(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	items, err := app.pipeline.ListQuestions(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func datasetIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}
