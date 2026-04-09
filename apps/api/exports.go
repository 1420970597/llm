package main

import (
  "fmt"
  "net/http"
  "net/url"
  "strconv"
  "strings"
  "time"

  "github.com/1420970597/llm/internal/model"
  "github.com/1420970597/llm/internal/storage"
)

func (app *application) enqueueExport(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }

  rewards, err := app.rewards.List(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  questions, err := app.pipeline.ListQuestions(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if len(rewards) == 0 {
    app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue export: dataset %d has no reward records", id))
    return
  }
  if len(rewards) < len(questions) {
    app.writeError(w, http.StatusConflict, fmt.Errorf("cannot enqueue export: dataset %d rewards incomplete (%d/%d)", id, len(rewards), len(questions)))
    return
  }

  enqueued, err := app.enqueueDatasetJob(r.Context(), "export.generate", id, "export_queued")
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  if enqueued {
    _ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "dataset_export", datasetIDString(id), "export.generate")
  }
  app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
    DatasetID:  id,
    Stage:      "export",
    State:      "queued",
    Message:    queuedMessage(enqueued, "导出任务已入队", "导出任务已在队列中"),
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

func (app *application) downloadArtifact(w http.ResponseWriter, r *http.Request) {
  id, err := datasetIDFromPath(r.URL.Path)
  if err != nil {
    app.writeError(w, http.StatusBadRequest, err)
    return
  }
  artifactID := r.URL.Query().Get("artifactId")
  if artifactID == "" {
    app.writeError(w, http.StatusBadRequest, fmt.Errorf("missing required query parameter: artifactId"))
    return
  }
  items, err := app.artifacts.List(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  dataset, err := app.datasets.GetDataset(r.Context(), id)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, err := app.datasets.ResolveStorageProfile(r.Context(), dataset.StorageProfileID)
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  objectStore, err := storage.New(storage.Profile{
    Endpoint:     endpoint,
    Region:       region,
    Bucket:       bucket,
    AccessKeyID:  accessKeyID,
    SecretKey:    secretKey,
    UsePathStyle: usePathStyle,
  })
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  for _, item := range items {
    if artifactID == strconv.FormatInt(item.ID, 10) {
      fileName := item.ObjectKey[strings.LastIndex(item.ObjectKey, "/")+1:]
      if fileName == "" {
        fileName = "dataset-export.jsonl"
      }
      objectKey := item.ObjectKey
      if parsed, parseErr := url.Parse(item.ObjectKey); parseErr == nil && parsed.Scheme == "s3" {
        objectKey = strings.TrimPrefix(parsed.Path, "/")
      }
      payload, err := objectStore.ReadBytes(r.Context(), objectKey)
      if err != nil {
        app.writeError(w, http.StatusInternalServerError, err)
        return
      }
      safeFileName := strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(fileName)
      w.Header().Set("Content-Type", item.ContentType)
      w.Header().Set("Content-Disposition", "attachment; filename=\""+safeFileName+"\"")
      _, _ = w.Write(payload)
      return
    }
  }
  http.NotFound(w, r)
}

func (app *application) runtimeStatus(w http.ResponseWriter, r *http.Request) {
  status, err := app.artifacts.Runtime(r.Context())
  if err != nil {
    app.writeError(w, http.StatusInternalServerError, err)
    return
  }
  app.writeJSON(w, http.StatusOK, status)
}
