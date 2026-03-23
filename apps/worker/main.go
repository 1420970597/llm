package main

import (
  "context"
  "encoding/json"
  "log"
  "net/http"
  "time"

  appcrypto "github.com/1420970597/llm/internal/crypto"
  "github.com/1420970597/llm/internal/config"
  "github.com/1420970597/llm/internal/llm"
  "github.com/1420970597/llm/internal/model"
  "github.com/1420970597/llm/internal/store"
  "github.com/jackc/pgx/v5/pgxpool"
  "github.com/redis/go-redis/v9"
)

type jobPayload struct {
  Type      string `json:"type"`
  DatasetID int64  `json:"datasetId"`
}

func main() {
  cfg := config.LoadWorkerConfig()
  ctx := context.Background()

  pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
  if err != nil {
    log.Fatalf("worker postgres connect failed: %v", err)
  }
  defer pool.Close()

  box, err := appcrypto.NewSecretBox(cfg.EncryptionKey)
  if err != nil {
    log.Fatalf("worker secret box init failed: %v", err)
  }

  datasets := store.NewDatasetStore(pool, box)
  pipeline := store.NewPipelineStore(pool)
  redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisHost + ":" + cfg.RedisPort})

  go consumeJobs(ctx, cfg.QueueName, redisClient, datasets, pipeline)

  mux := http.NewServeMux()
  mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "worker"})
  })

  server := &http.Server{
    Addr:              ":" + cfg.Port,
    Handler:           mux,
    ReadHeaderTimeout: 5 * time.Second,
  }

  log.Printf("worker listening on :%s", cfg.Port)
  if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
    log.Fatalf("worker server failed: %v", err)
  }
}

func consumeJobs(ctx context.Context, queue string, redisClient *redis.Client, datasets *store.DatasetStore, pipeline *store.PipelineStore) {
  for {
    result, err := redisClient.BRPop(ctx, 5*time.Second, queue).Result()
    if err != nil {
      if err == redis.Nil {
        continue
      }
      log.Printf("worker queue read failed: %v", err)
      time.Sleep(2 * time.Second)
      continue
    }
    if len(result) != 2 {
      continue
    }

    var job jobPayload
    if err := json.Unmarshal([]byte(result[1]), &job); err != nil {
      log.Printf("worker payload decode failed: %v", err)
      continue
    }

    switch job.Type {
    case "questions.generate":
      if err := handleQuestionGeneration(ctx, job.DatasetID, datasets, pipeline); err != nil {
        log.Printf("question generation failed dataset=%d err=%v", job.DatasetID, err)
      }
    default:
      log.Printf("worker ignored job type=%s", job.Type)
    }
  }
}

func handleQuestionGeneration(ctx context.Context, datasetID int64, datasets *store.DatasetStore, pipeline *store.PipelineStore) error {
  dataset, err := datasets.GetDataset(ctx, datasetID)
  if err != nil {
    return err
  }
  domains, err := datasets.ListDomains(ctx, datasetID)
  if err != nil {
    return err
  }
  if len(domains) == 0 {
    return nil
  }

  baseURL, modelName, providerType, apiKey, err := datasets.ResolveProvider(ctx, dataset.ProviderID)
  if err != nil {
    return err
  }

  questions, err := llm.GenerateQuestions(ctx, llm.ProviderConfig{
    BaseURL:      baseURL,
    Model:        modelName,
    ProviderType: providerType,
    APIKey:       apiKey,
  }, dataset, domains)
  if err != nil {
    return err
  }
  if err := pipeline.InsertQuestions(ctx, datasetID, questions); err != nil {
    return err
  }
  log.Printf("questions generated dataset=%d count=%d", datasetID, len(questions))
  return nil
}

var _ = model.Question{}
