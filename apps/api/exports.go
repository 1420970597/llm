package main

import (
  "encoding/json"
  "net/http"
  "time"

  "github.com/1420970597/llm/internal/model"
)

func (app *application) enqueueExport(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }
  payload, _ := json.Marshal(map[string]any{"type": "export.generate", "datasetId": id})
  if err := app.redis.LPush(r.Context(), app.cfg.QueueName, payload).Err(); err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if err := app.datasets.UpdateStatus(r.Context(), id, "export_queued"); err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  _ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "dataset_export", datasetIDString(id), "export.generate")
  app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
    DatasetID:  id,
    Stage:      "export",
    State:      "queued",
    Message:    "导出任务已入队",
    AcceptedAt: time.Now().Format(time.RFC3339),
  })
}

func (app *application) listArtifacts(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }
  items, err := app.artifacts.List(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  app.writeJSON(w, http.StatusOK, items)
}

func (app *application) runtimeStatus(w http.ResponseWriter, r *http.Request) {
  status, err := app.artifacts.Runtime(r.Context())
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  app.writeJSON(w, http.StatusOK, status)
}
