package main

import (
  "context"
  "encoding/json"
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

func (app *application) enqueueDatasetJob(ctx context.Context, jobType string, datasetID int64, queuedStatus string) (bool, error) {
  queued, err := app.redis.LRange(ctx, app.cfg.QueueName, 0, -1).Result()
  if err != nil {
    return false, err
  }
  for _, raw := range queued {
    var existing map[string]any
    if err := json.Unmarshal([]byte(raw), &existing); err != nil {
      continue
    }
    if existing["type"] == jobType && int64FromAny(existing["datasetId"]) == datasetID {
      return false, nil
    }
  }

  payload, err := json.Marshal(map[string]any{"type": jobType, "datasetId": datasetID})
  if err != nil {
    return false, err
  }
  if err := app.redis.LPush(ctx, app.cfg.QueueName, payload).Err(); err != nil {
    return false, err
  }
  if err := app.datasets.UpdateStatus(ctx, datasetID, queuedStatus); err != nil {
    return false, err
  }
  return true, nil
}

func int64FromAny(value any) int64 {
  switch typed := value.(type) {
  case float64:
    return int64(typed)
  case int64:
    return typed
  case int:
    return int64(typed)
  default:
    return 0
  }
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
