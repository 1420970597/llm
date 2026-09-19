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
	"github.com/1420970597/llm/internal/migrate"
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

	if err := migrate.Run(ctx, pool, cfg.MigrationPath); err != nil {
		log.Fatalf("worker migration failed: %v", err)
	}

	box, err := appcrypto.NewSecretBox(cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("worker secret box init failed: %v", err)
	}

	datasets := store.NewDatasetStore(pool, box)
	pipeline := store.NewPipelineStore(pool)
	reasoningStore := store.NewReasoningStore(pool)
	if err := reasoningStore.EnsureSchemaReady(ctx); err != nil {
		log.Fatalf("worker reasoning schema readiness failed: %v", err)
	}
	rewardStore := store.NewRewardStore(pool)
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisHost + ":" + cfg.RedisPort})
	promptStore := store.NewAdminStore(pool, box)
	artifactStore := store.NewArtifactStore(pool, redisClient, cfg.QueueName)
	generationRunStore := store.NewGenerationRunStore(pool)

	jobCtx := &jobContext{
		queue:          cfg.QueueName,
		redis:          redisClient,
		datasets:       datasets,
		pipeline:       pipeline,
		prompts:        promptStore,
		reasoning:      reasoningStore,
		rewards:        rewardStore,
		artifacts:      artifactStore,
		generationRuns: generationRunStore,
	}

	go consumeJobs(ctx, jobCtx)

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

func consumeJobs(ctx context.Context, jc *jobContext) {
	for {
		result, err := jc.redis.BRPop(ctx, 5*time.Second, jc.queue).Result()
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

		// 先查注册表（lane 新增的 job 类型），未命中再走 legacy 分支。
		if handler, found := lookupJobHandler(job.Type); found {
			if err := handler(ctx, jc, job); err != nil {
				if shouldRetryJob(err) && job.Retry < 2 {
					next := job
					next.Retry++
					if requeueErr := requeueJob(ctx, jc.redis, jc.queue, next); requeueErr != nil {
						log.Printf("job failed dataset=%d type=%s retry=%d err=%v requeue_err=%v", job.DatasetID, job.Type, job.Retry, err, requeueErr)
						continue
					}
					log.Printf("job retrying dataset=%d type=%s next_retry=%d err=%v", job.DatasetID, job.Type, next.Retry, err)
					continue
				}
				markStageFailed(ctx, jc.datasets, job.DatasetID, job.Type+"_failed", err)
			}
			continue
		}

		switch job.Type {
		case "questions.generate":
			if err := handleQuestionGeneration(ctx, job.DatasetID, jc.datasets, jc.pipeline, jc.prompts); err != nil {
				markStageFailed(ctx, jc.datasets, job.DatasetID, "questions_failed", err)
			}
		case "reasoning.generate":
			if err := handleReasoningGeneration(ctx, job.DatasetID, jc.datasets, jc.pipeline, jc.prompts, jc.reasoning); err != nil {
				if shouldRetryJob(err) && job.Retry < 2 {
					next := job
					next.Retry++
					if requeueErr := requeueJob(ctx, jc.redis, jc.queue, next); requeueErr != nil {
						markStageFailed(ctx, jc.datasets, job.DatasetID, "reasoning_failed", err)
						continue
					}
					log.Printf("reasoning generation retrying dataset=%d next_retry=%d err=%v", job.DatasetID, next.Retry, err)
					continue
				}
				markStageFailed(ctx, jc.datasets, job.DatasetID, "reasoning_failed", err)
			}
		case "rewards.generate":
			if err := handleRewardGeneration(ctx, job.DatasetID, jc.datasets, jc.pipeline, jc.prompts, jc.rewards); err != nil {
				if shouldRetryJob(err) && job.Retry < 2 {
					next := job
					next.Retry++
					if requeueErr := requeueJob(ctx, jc.redis, jc.queue, next); requeueErr != nil {
						markStageFailed(ctx, jc.datasets, job.DatasetID, "rewards_failed", err)
						continue
					}
					log.Printf("reward generation retrying dataset=%d next_retry=%d err=%v", job.DatasetID, next.Retry, err)
					continue
				}
				markStageFailed(ctx, jc.datasets, job.DatasetID, "rewards_failed", err)
			}
		case "export.generate":
			if err := handleExportGeneration(ctx, job.DatasetID, jc.datasets, jc.pipeline, jc.reasoning, jc.rewards, jc.artifacts); err != nil {
				markStageFailed(ctx, jc.datasets, job.DatasetID, "export_failed", err)
			}
		default:
			log.Printf("worker ignored job type=%s", job.Type)
		}
	}
}

func handleQuestionGeneration(ctx context.Context, datasetID int64, datasets *store.DatasetStore, pipeline *store.PipelineStore, promptStore *store.AdminStore) error {
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

	promptTemplate, promptErr := promptStore.GetActivePromptByStage(ctx, "question-generation")
	var promptConfig *model.PromptTemplate
	if promptErr == nil {
		promptConfig = &promptTemplate
	}

	questions, err := llm.GenerateQuestions(ctx, llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
	}, dataset, domains, promptConfig)
	if err != nil {
		return err
	}
	if err := pipeline.InsertQuestions(ctx, datasetID, questions); err != nil {
		return err
	}
	log.Printf("questions generated dataset=%d count=%d", datasetID, len(questions))
	return nil
}

func handleReasoningGeneration(ctx context.Context, datasetID int64, datasets *store.DatasetStore, pipeline *store.PipelineStore, promptStore *store.AdminStore, reasoningStore *store.ReasoningStore) error {
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

	promptTemplate, promptErr := promptStore.GetActivePromptByStage(ctx, "reasoning-generation")
	var promptConfig *model.PromptTemplate
	if promptErr == nil {
		promptConfig = &promptTemplate
	}

	// 关键（issue #5）：答案记录必须**整批一次**落库。
	//
	// internal/store 的 Insert 语义是「整批完成 + 推进数据集状态」：它按传入的这批
	// 记录统计 failed/partial 并写死 datasets.status。若在逐题循环内调用，第一次迭代
	// 就会用「当次那一条」推算并写死整批终态 —— 后续题目无论成败都改不回真实状态。
	// 因此这里先累积全部题目，循环结束后才 flushOnce。
	batch := &batchPersist[model.ReasoningRecord]{}
	for _, question := range questions {
		records, payloads, err := llm.GenerateReasoning(ctx, llm.ProviderConfig{
			BaseURL:         baseURL,
			Model:           modelName,
			ProviderType:    providerType,
			ReasoningEffort: reasoningEffort,
			APIKey:          apiKey,
		}, dataset, []model.Question{question}, promptConfig)
		if err != nil {
			// 中途失败：保住已经拿到的记录，但**不推进状态**（salvage 用 UpsertPartial）。
			// 调用方随后会把数据集标为 reasoning_failed，这是正确的：这一批并没有跑完。
			batch.salvage(func(items []model.ReasoningRecord) error {
				return reasoningStore.UpsertPartial(ctx, datasetID, items)
			})
			return err
		}
		for index := range records {
			key := filepath.ToSlash(fmt.Sprintf("datasets/%d/reasoning/question-%d.json", datasetID, records[index].QuestionID))
			uri, putErr := objectStore.PutJSON(ctx, key, payloads[records[index].QuestionID])
			if putErr != nil {
				batch.salvage(func(items []model.ReasoningRecord) error {
					return reasoningStore.UpsertPartial(ctx, datasetID, items)
				})
				return putErr
			}
			records[index].ObjectKey = uri
		}
		batch.add(records...)
	}
	if err := batch.flushOnce(func(items []model.ReasoningRecord) error {
		return reasoningStore.Insert(ctx, datasetID, items)
	}); err != nil {
		return err
	}
	log.Printf("reasoning generated dataset=%d count=%d", datasetID, batch.len())
	return nil
}

func handleRewardGeneration(ctx context.Context, datasetID int64, datasets *store.DatasetStore, pipeline *store.PipelineStore, promptStore *store.AdminStore, rewardStore *store.RewardStore) error {
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

	promptTemplate, promptErr := promptStore.GetActivePromptByStage(ctx, "reward-generation")
	var promptConfig *model.PromptTemplate
	if promptErr == nil {
		promptConfig = &promptTemplate
	}

	// 关键（issue #5）：同 handleReasoningGeneration —— 评分记录整批一次落库，
	// 否则第一条写完就会把 datasets.status 写死成 rewards_generated。
	batch := &batchPersist[model.RewardRecord]{}
	for _, question := range questions {
		records, payloads, err := llm.GenerateRewards(ctx, llm.ProviderConfig{
			BaseURL:         baseURL,
			Model:           modelName,
			ProviderType:    providerType,
			ReasoningEffort: reasoningEffort,
			APIKey:          apiKey,
		}, dataset, []model.Question{question}, promptConfig)
		if err != nil {
			batch.salvage(func(items []model.RewardRecord) error {
				return rewardStore.UpsertPartial(ctx, datasetID, items)
			})
			return err
		}
		for index := range records {
			key := filepath.ToSlash(fmt.Sprintf("datasets/%d/rewards/question-%d.json", datasetID, records[index].QuestionID))
			uri, putErr := objectStore.PutJSON(ctx, key, payloads[records[index].QuestionID])
			if putErr != nil {
				batch.salvage(func(items []model.RewardRecord) error {
					return rewardStore.UpsertPartial(ctx, datasetID, items)
				})
				return putErr
			}
			records[index].ObjectKey = uri
		}
		batch.add(records...)
	}
	if err := batch.flushOnce(func(items []model.RewardRecord) error {
		return rewardStore.Insert(ctx, datasetID, items)
	}); err != nil {
		return err
	}
	log.Printf("reward records generated dataset=%d count=%d", datasetID, batch.len())
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
		// 契约 §1.3：只有 generated 可进入导出。用 model 里的统一判定而不是
		// 逐个比较字符串 —— `!= "failed"` 会把新增的 `invalid`（占位内容）放行，
		// 这正是 issue #7 在出口处失守的原因。
		if !hasReasoning || !hasReward ||
			!model.RecordStatusUsableForDownstream(reasoning.Status) ||
			!model.RecordStatusUsableForDownstream(reward.Status) {
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
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal export record dataset=%d question=%d: %w", datasetID, question.ID, err)
		}
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
