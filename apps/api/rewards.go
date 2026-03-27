package main

import (
  "fmt"
  "net/http"
  "time"

  "github.com/1420970597/llm/internal/model"
)

func (app *application) enqueueRewardGeneration(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }

  reasoning, err := app.reasoning.List(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  questions, err := app.pipeline.ListQuestions(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if len(reasoning) == 0 {
    app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue rewards: dataset %d has no reasoning records", id))
    return
  }
  if len(reasoning) < len(questions) {
    app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue rewards: dataset %d reasoning incomplete (%d/%d)", id, len(reasoning), len(questions)))
    return
  }

  enqueued, err := app.enqueueDatasetJob(r.Context(), "rewards.generate", id, "rewards_queued")
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if enqueued {
    _ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "reward_generation", datasetIDString(id), "rewards.generate")
  }
  app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
    DatasetID:  id,
    Stage:      "rewards",
    State:      "queued",
    Message:    queuedMessage(enqueued, "奖励评估任务已入队", "奖励评估任务已在队列中"),
    AcceptedAt: time.Now().Format(time.RFC3339),
  })
}

func (app *application) listRewards(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }
  items, err := app.rewards.List(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  app.writeJSON(w, http.StatusOK, items)
}
