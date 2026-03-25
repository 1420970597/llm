package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/config"
	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/storage"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type jobPayload struct {
	Type      string `json:"type"`
	DatasetID int64  `json:"datasetId"`
	Retry     int    `json:"retry,omitempty"`
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
	reasoningStore := store.NewReasoningStore(pool)
	rewardStore := store.NewRewardStore(pool)
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisHost + ":" + cfg.RedisPort})
	artifactStore := store.NewArtifactStore(pool, redisClient, cfg.QueueName)

	go consumeJobs(ctx, cfg.QueueName, redisClient, datasets, pipeline, reasoningStore, rewardStore, artifactStore)

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

func consumeJobs(ctx context.Context, queue string, redisClient *redis.Client, datasets *store.DatasetStore, pipeline *store.PipelineStore, reasoningStore *store.ReasoningStore, rewardStore *store.RewardStore, artifactStore *store.ArtifactStore) {
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
		case "reasoning.generate":
			if err := handleReasoningGeneration(ctx, job.DatasetID, datasets, pipeline, reasoningStore); err != nil {
				if shouldRetryJob(err) && job.Retry < 2 {
					next := job
					next.Retry++
					if requeueErr := requeueJob(ctx, redisClient, queue, next); requeueErr != nil {
						log.Printf("reasoning generation failed dataset=%d retry=%d err=%v requeue_err=%v", job.DatasetID, job.Retry, err, requeueErr)
						continue
					}
					log.Printf("reasoning generation retrying dataset=%d next_retry=%d err=%v", job.DatasetID, next.Retry, err)
					continue
				}
				log.Printf("reasoning generation failed dataset=%d retry=%d err=%v", job.DatasetID, job.Retry, err)
			}
		case "rewards.generate":
			if err := handleRewardGeneration(ctx, job.DatasetID, datasets, pipeline, rewardStore); err != nil {
				if shouldRetryJob(err) && job.Retry < 2 {
					next := job
					next.Retry++
					if requeueErr := requeueJob(ctx, redisClient, queue, next); requeueErr != nil {
						log.Printf("reward generation failed dataset=%d retry=%d err=%v requeue_err=%v", job.DatasetID, job.Retry, err, requeueErr)
						continue
					}
					log.Printf("reward generation retrying dataset=%d next_retry=%d err=%v", job.DatasetID, next.Retry, err)
					continue
				}
				log.Printf("reward generation failed dataset=%d retry=%d err=%v", job.DatasetID, job.Retry, err)
			}
		case "export.generate":
			if err := handleExportGeneration(ctx, job.DatasetID, datasets, pipeline, reasoningStore, rewardStore, artifactStore); err != nil {
				log.Printf("export generation failed dataset=%d err=%v", job.DatasetID, err)
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

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		return err
	}

	questions, err := llm.GenerateQuestions(ctx, llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
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

func handleReasoningGeneration(ctx context.Context, datasetID int64, datasets *store.DatasetStore, pipeline *store.PipelineStore, reasoningStore *store.ReasoningStore) error {
	dataset, err := datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return err
	}
	questions, err := pipeline.ListQuestions(ctx, datasetID)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		return nil
	}

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		return err
	}
	endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, err := datasets.ResolveStorageProfile(ctx, dataset.StorageProfileID)
	if err != nil {
		return err
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
		return err
	}

	recorded := 0
	for _, question := range questions {
		records, payloads, err := llm.GenerateReasoning(ctx, llm.ProviderConfig{
			BaseURL:         baseURL,
			Model:           modelName,
			ProviderType:    providerType,
			ReasoningEffort: reasoningEffort,
			APIKey:          apiKey,
		}, dataset, []model.Question{question})
		if err != nil {
			return err
		}
		for index := range records {
			key := filepath.ToSlash(fmt.Sprintf("datasets/%d/reasoning/question-%d.json", datasetID, records[index].QuestionID))
			uri, putErr := objectStore.PutJSON(ctx, key, payloads[records[index].QuestionID])
			if putErr != nil {
				return putErr
			}
			records[index].ObjectKey = uri
			if upsertErr := reasoningStore.UpsertPartial(ctx, datasetID, []model.ReasoningRecord{records[index]}); upsertErr != nil {
				return upsertErr
			}
			recorded++
		}
	}
	if err := datasets.UpdateStatus(ctx, datasetID, "reasoning_generated"); err != nil {
		return err
	}
	log.Printf("reasoning generated dataset=%d count=%d", datasetID, recorded)
	return nil
}

func handleRewardGeneration(ctx context.Context, datasetID int64, datasets *store.DatasetStore, pipeline *store.PipelineStore, rewardStore *store.RewardStore) error {
	dataset, err := datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return err
	}
	questions, err := pipeline.ListQuestions(ctx, datasetID)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		return nil
	}

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		return err
	}
	endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, err := datasets.ResolveStorageProfile(ctx, dataset.StorageProfileID)
	if err != nil {
		return err
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
		return err
	}

	recorded := 0
	for _, question := range questions {
		records, payloads, err := llm.GenerateRewards(ctx, llm.ProviderConfig{
			BaseURL:         baseURL,
			Model:           modelName,
			ProviderType:    providerType,
			ReasoningEffort: reasoningEffort,
			APIKey:          apiKey,
		}, dataset, []model.Question{question})
		if err != nil {
			return err
		}
		for index := range records {
			key := filepath.ToSlash(fmt.Sprintf("datasets/%d/rewards/question-%d.json", datasetID, records[index].QuestionID))
			uri, putErr := objectStore.PutJSON(ctx, key, payloads[records[index].QuestionID])
			if putErr != nil {
				return putErr
			}
			records[index].ObjectKey = uri
			if upsertErr := rewardStore.UpsertPartial(ctx, datasetID, []model.RewardRecord{records[index]}); upsertErr != nil {
				return upsertErr
			}
			recorded++
		}
	}
	if err := datasets.UpdateStatus(ctx, datasetID, "rewards_generated"); err != nil {
		return err
	}
	log.Printf("reward records generated dataset=%d count=%d", datasetID, recorded)
	return nil
}

func handleExportGeneration(ctx context.Context, datasetID int64, datasets *store.DatasetStore, pipeline *store.PipelineStore, reasoningStore *store.ReasoningStore, rewardStore *store.RewardStore, artifactStore *store.ArtifactStore) error {
	dataset, err := datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return err
	}
	questions, err := pipeline.ListQuestions(ctx, datasetID)
	if err != nil {
		return err
	}
	reasoningRecords, err := reasoningStore.List(ctx, datasetID)
	if err != nil {
		return err
	}
	rewardRecords, err := rewardStore.List(ctx, datasetID)
	if err != nil {
		return err
	}
	endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, err := datasets.ResolveStorageProfile(ctx, dataset.StorageProfileID)
	if err != nil {
		return err
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
		return err
	}

	reasoningByQuestion := map[int64]model.ReasoningRecord{}
	for _, record := range reasoningRecords {
		reasoningByQuestion[record.QuestionID] = record
	}
	rewardByQuestion := map[int64]model.RewardRecord{}
	for _, record := range rewardRecords {
		rewardByQuestion[record.QuestionID] = record
	}

	lines := make([]byte, 0, len(questions)*256)
	exportedCount := 0
	for _, question := range questions {
		reasoning, hasReasoning := reasoningByQuestion[question.ID]
		reward, hasReward := rewardByQuestion[question.ID]
		if !hasReasoning || !hasReward {
			continue
		}
		payload := map[string]any{
			"dataset_id":       datasetID,
			"question_id":      question.ID,
			"question":         question.Content,
			"domain_name":      question.DomainName,
			"answer_summary":   reasoning.AnswerSummary,
			"reasoning_object": reasoning.ObjectKey,
			"reward_score":     reward.Score,
			"reward_object":    reward.ObjectKey,
		}
		body, _ := json.Marshal(payload)
		lines = append(lines, body...)
		lines = append(lines, '\n')
		exportedCount++
	}
	if exportedCount == 0 {
		return fmt.Errorf("no complete records to export for dataset %d", datasetID)
	}

	key := filepath.ToSlash(fmt.Sprintf("datasets/%d/exports/dataset.jsonl", datasetID))
	uri, err := objectStore.PutBytes(ctx, key, lines, "application/jsonl")
	if err != nil {
		return err
	}
	_, err = artifactStore.Insert(ctx, model.Artifact{
		DatasetID:    datasetID,
		ArtifactType: "jsonl-export",
		ObjectKey:    uri,
		ContentType:  "application/jsonl",
	})
	if err != nil {
		return err
	}
	if err := datasets.UpdateStatus(ctx, datasetID, "export_generated"); err != nil {
		return err
	}
	log.Printf("export generated dataset=%d artifact=%s", datasetID, uri)
	return nil
}

func shouldRetryJob(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, " timeout") ||
		strings.Contains(message, "deadline exceeded") ||
		strings.Contains(message, "502") ||
		strings.Contains(message, "503") ||
		strings.Contains(message, "504") ||
		strings.Contains(message, "429")
}

func requeueJob(ctx context.Context, redisClient *redis.Client, queue string, job jobPayload) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return redisClient.LPush(ctx, queue, payload).Err()
}

var _ = model.Question{}
