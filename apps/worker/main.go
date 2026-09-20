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

// failedStatusForJob 把「job 类型」映射为**进度接口认识的**数据集失败状态。
//
// 为什么需要它（issue #140，父代理复核时发现是系统性问题）：
// 这里此前写的是 `job.Type + "_failed"`，而注册表里的 job 类型都带点号：
//
//	export.generate   -> export.generate_failed
//	sft.generate      -> sft.generate_failed
//	grpo.generate     -> grpo.generate_failed
//	questions.generate-> questions.generate_failed
//	eval.run          -> eval.run_failed
//	cleaning.run      -> cleaning.run_failed
//	directions.generate -> directions.generate_failed
//	chain-standards.generate -> chain-standards.generate_failed
//
// 而进度接口（internal/store/dataset_store.go 的 rankByStatus / failedStageByStatus /
// completionByStatus）只认识**下划线形态**（export_failed / questions_failed / ...）。
// 于是这些失败状态在界面上退化成「没有任何阶段失败、完成度 0%、阶段仍在进行中」——
// 实测 dataset 161（export.generate_failed）显示为：
//
//	status=export.generate_failed  completion=0  currentStage=domains
//	domains in_progress(40)  questions pending  ... export pending   ← 无 failed
//
// 用户看到的是「进行中 0%」，既不知道失败了，也不知道卡在哪。
//
// 修法：集中映射一次，**默认回退到 job.Type + "_failed"**（保持未知类型可观测），
// 已知类型一律映射到进度接口认识的规范名。
func failedStatusForJob(jobType string) string {
	switch jobType {
	case "directions.generate":
		return "directions_partial_failed"
	case questionsJobType:
		return "questions_failed"
	case "chain-standards.generate":
		return "chain_standards_failed"
	case "grpo.generate":
		return "grpo_failed"
	case "sft.generate":
		return "sft_failed"
	case "export.generate":
		return "export_failed"
	case evalRunStage:
		return "eval_failed"
	case cleaningRunStage:
		return "cleaning_failed"
	default:
		return jobType + "_failed"
	}
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

	// 恢复「状态声称在排队、但队列里已经没有」的任务（issue #139 / #143）。
	//
	// 为什么需要：队列是 Redis List + BRPOP（**破坏性读取，无 ack**），且 Redis
	// 曾关闭持久化。Redis/worker 重启后，尚未出队的任务会永久消失，
	// 而 datasets.status 仍停在 *_queued —— 用户看到「一直在排队」，
	// 实际队列里根本没有它，永远不会再被处理（实测最长停留 23 小时）。
	//
	// 修法：worker 启动时扫一遍「排队中」的数据集，把它们**重新入队**。
	// 这里刻意不做「先比对队列内容」：BRPOP 是破坏性的，正在被其他 worker
	// 处理的任务也不在队列里，比对会把在途任务重复入队；
	// 而重新入队是**幂等安全**的 —— 各阶段的处理器本身按数据集状态做守卫
	//（例如问题生成会检查上游是否就绪、导出会检查内容是否齐备），
	// 重复入队最多触发一次「不满足前置条件」的快速失败，不会产生重复数据。
	go recoverStalledJobs(ctx, jobCtx)

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

// queuedStatusToJobType 把「数据集排队状态」映射回「入队时用的 job 类型」。
//
// 与 apps/api 的入队点一一对应（见各自路由里的 enqueueJob(..., "<type>", id, "<status>")）：
//
//	directions_queued       <- directions.generate
//	chain_standards_queued  <- chain-standards.generate
//	questions_queued        <- questions.generate
//	reasoning_queued        <- reasoning.generate
//	rewards_queued          <- rewards.generate
//	grpo_queued             <- grpo.generate
//	sft_queued              <- sft.generate
//	export_queued           <- export.generate
//
// 注意 eval.run / cleaning.run **不在其中**：它们的入队点传的排队状态是空串
// （见 routes_eval_runs.go / routes_cleaning_runs.go），不写 datasets.status，
// 因此不存在「状态说在排队但队列里没有」这种形态。
var queuedStatusToJobType = map[string]string{
	"directions_queued":      "directions.generate",
	"chain_standards_queued": "chain-standards.generate",
	"questions_queued":       "questions.generate",
	"reasoning_queued":       "reasoning.generate",
	"rewards_queued":         "rewards.generate",
	"grpo_queued":            "grpo.generate",
	"sft_queued":             "sft.generate",
	"export_queued":          "export.generate",
}

// recoverStalledJobs 把「状态声称在排队」的数据集重新入队一次。
//
// 为什么需要它（issue #139 / #143）：队列是 Redis List + BRPOP（破坏性读取、无 ack），
// 且 Redis 曾关闭持久化。Redis 或 worker 重启后，尚未出队的任务永久消失，
// 而 datasets.status 仍停在 *_queued —— 用户看到「一直在排队」，
// 实际队列里没有它，永远不会再被处理（实测最长停留 23 小时，LLEN=0）。
//
// 为什么直接重新入队而不是「先比对队列内容」：BRPOP 是破坏性的，正在被处理的
// 任务同样不在队列里，比对会把**在途任务重复入队**。而重新入队是幂等安全的：
// 各阶段处理器本身按前置条件做守卫（问题生成检查上游、导出检查内容齐备），
// 重复入队最多触发一次快速失败，不会产生重复数据。
//
// 为什么在启动时做一次而不是定时轮询：这个恢复动作的语义是「服务重启后拾起丢失的任务」，
// 启动时扫一遍即可覆盖；定时轮询会在长时间运行中反复入队业务上已放弃的任务
// （例如用户主动不想要的阶段），那不是恢复而是打扰。
func recoverStalledJobs(ctx context.Context, jc *jobContext) {
	stalled, err := jc.datasets.ListStalledQueuedDatasets(ctx)
	if err != nil {
		log.Printf("recover.stalled.list_failed err=%v", err)
		return
	}
	if len(stalled) == 0 {
		return
	}

	recovered := 0
	for _, dataset := range stalled {
		jobType, ok := queuedStatusToJobType[dataset.Status]
		if !ok {
			continue
		}
		if err := requeueJob(ctx, jc.redis, jc.queue, jobPayload{Type: jobType, DatasetID: dataset.ID}); err != nil {
			log.Printf("recover.stalled.requeue_failed dataset=%d status=%s type=%s err=%v",
				dataset.ID, dataset.Status, jobType, err)
			continue
		}
		recovered++
		log.Printf("recover.stalled.requeued dataset=%d status=%s type=%s", dataset.ID, dataset.Status, jobType)
	}
	if recovered > 0 {
		log.Printf("recover.stalled.done stalled=%d recovered=%d", len(stalled), recovered)
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
				markStageFailed(ctx, jc.datasets, job.DatasetID, failedStatusForJob(job.Type), err)
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
