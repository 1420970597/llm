package main

import (
  "encoding/json"
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

  payload, _ := json.Marshal(map[string]any{"type": "questions.generate", "datasetId": id})
  if err := app.redis.LPush(r.Context(), app.cfg.QueueName, payload).Err(); err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if err := app.datasets.UpdateStatus(r.Context(), id, "questions_queued"); err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  _ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "question_generation", datasetIDString(id), "questions.generate")
  app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
    DatasetID:  id,
    Stage:      "questions",
    State:      "queued",
    Message:    "问题生成任务已入队",
    AcceptedAt: time.Now().Format(time.RFC3339),
  })
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
