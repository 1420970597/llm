package main

import (
  "encoding/json"
  "net/http"
  "time"

  "github.com/1420970597/llm/internal/model"
)

func (app *application) enqueueReasoningGeneration(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }

  payload, _ := json.Marshal(map[string]any{"type": "reasoning.generate", "datasetId": id})
  if err := app.redis.LPush(r.Context(), app.cfg.QueueName, payload).Err(); err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if err := app.datasets.UpdateStatus(r.Context(), id, "reasoning_queued"); err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  _ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "reasoning_generation", datasetIDString(id), "reasoning.generate")
  app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
    DatasetID:  id,
    Stage:      "reasoning",
    State:      "queued",
    Message:    "推理生成任务已入队",
    AcceptedAt: time.Now().Format(time.RFC3339),
  })
}

func (app *application) listReasoning(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }
  items, err := app.reasoning.List(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  app.writeJSON(w, http.StatusOK, items)
}
