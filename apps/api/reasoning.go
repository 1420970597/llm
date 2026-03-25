package main

import (
  "fmt"
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

  questions, err := app.pipeline.ListQuestions(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if len(questions) == 0 {
    app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue reasoning: dataset %d has no questions", id))
    return
  }

  enqueued, err := app.enqueueDatasetJob(r.Context(), "reasoning.generate", id, "reasoning_queued")
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if enqueued {
    _ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "reasoning_generation", datasetIDString(id), "reasoning.generate")
  }
  app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
    DatasetID:  id,
    Stage:      "reasoning",
    State:      "queued",
    Message:    queuedMessage(enqueued, "推理生成任务已入队", "推理生成任务已在队列中"),
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
