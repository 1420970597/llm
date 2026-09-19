package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/1420970597/llm/internal/exporter"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/storage"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L6 lane 独占。通过 job 注册表接入，不修改 worker/main.go。

func init() {
	RegisterJobHandler("export.generate", handleMultiFormatExport)
}

// saveExportRequest 由 API 在入队前调用，把请求写入 Redis。
func saveExportRequest(ctx context.Context, jc *jobContext, datasetID int64, request exporter.Request) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return jc.redis.Set(ctx, exporter.RequestKey(datasetID), payload, exporter.RequestTTL).Err()
}

// loadExportRequest 由 worker 出队后调用，取回请求。
// 键不存在时返回空请求，导出退化为该格式的内置默认字段与映射。
func loadExportRequest(ctx context.Context, jc *jobContext, datasetID int64) (exporter.Request, error) {
	raw, err := jc.redis.Get(ctx, exporter.RequestKey(datasetID)).Result()
	if err != nil {
		return exporter.Request{}, nil
	}
	var request exporter.Request
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		return exporter.Request{}, err
	}
	return request, nil
}

// handleMultiFormatExport 按请求的格式与字段映射导出数据集。
//
// 与 legacy 的 handleExportGeneration 并存：legacy 仍处理未指定格式的旧请求，
// 本处理器处理带 format 的新请求（含显式 format=jsonl 且无映射的请求）。
func handleMultiFormatExport(ctx context.Context, jc *jobContext, job jobPayload) error {
	datasetID := job.DatasetID

	spec, err := loadExportRequest(ctx, jc, datasetID)
	if err != nil {
		return fmt.Errorf("读取导出请求失败 dataset=%d: %w", datasetID, err)
	}

	// 未指定格式时回退到 legacy 行为，保证既有导出流程零回退。
	if spec.Format == "" {
		return handleExportGeneration(ctx, datasetID, jc.datasets, jc.pipeline, jc.reasoning, jc.rewards, jc.artifacts)
	}
	exp, ok := exporter.Get(spec.Format)
	if !ok {
		return fmt.Errorf("不支持的导出格式 dataset=%d format=%s", datasetID, spec.Format)
	}

	mappingStore := store.NewExportMappingStore(jc.db())
	mapping, err := mappingStore.ResolveDefault(ctx, spec.Format, spec.MappingID)
	if err != nil {
		return fmt.Errorf("解析字段映射失败 dataset=%d mapping=%d: %w", datasetID, spec.MappingID, err)
	}

	records, skipped, err := loadExportRecords(ctx, jc, datasetID, spec.Filters, mapping.TargetKind)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("数据集 %d 没有可导出的记录（过滤后为空）", datasetID)
	}

	payload, err := exp.Encode(records, mapping)
	if err != nil {
		return fmt.Errorf("编码导出内容失败 dataset=%d format=%s: %w", datasetID, spec.Format, err)
	}

	dataset, err := jc.datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return err
	}
	endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, err := jc.datasets.ResolveStorageProfile(ctx, dataset.StorageProfileID)
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

	// 文件名带格式与映射名，同一数据集导出多种格式时不会互相覆盖。
	key := filepath.ToSlash(fmt.Sprintf("datasets/%d/exports/dataset-%s%s", datasetID, spec.Format, exp.Ext()))
	uri, err := objectStore.PutBytes(ctx, key, payload, exp.ContentType())
	if err != nil {
		return err
	}

	artifactType := spec.Format + "-export"
	if _, err := jc.artifacts.Insert(ctx, model.Artifact{
		DatasetID:    datasetID,
		ArtifactType: artifactType,
		ObjectKey:    uri,
		ContentType:  exp.ContentType(),
	}); err != nil {
		return err
	}

	if err := jc.datasets.UpdateStatus(ctx, datasetID, "export_generated"); err != nil {
		return err
	}

	// 导出完成后清掉请求键，避免同一请求被后续任务重复使用。
	_ = jc.redis.Del(ctx, exporter.RequestKey(datasetID)).Err()

	log.Printf("multi-format export done dataset=%d format=%s records=%d skipped=%d mapping=%s artifact=%s",
		datasetID, spec.Format, len(records), skipped, mapping.Name, uri)
	return nil
}

// loadExportRecords 从各来源装配统一导出记录。
//
// 数据来源与优先级：
//   - 问题：questions 表（必选，是导出的行主键）
//   - 思维链与答案：优先 sft_records（SFT 一等公民表），回退 reasoning_records
//   - 评判提示词：grpo_prompts
//   - 打分：reward_records
//
// targetKind 决定「什么算有内容」：SFT 导出需要思维链或答案，GRPO 导出需要评判
// 提示词。若不加区分，对只生成了评判提示词的数据集做 GRPO 导出会把全部记录
// 当作空记录丢弃。
//
// 返回的 skipped 是因内容不足而被过滤掉的条数。
func loadExportRecords(ctx context.Context, jc *jobContext, datasetID int64, filters map[string]any, targetKind string) ([]exporter.Record, int, error) {
	dataset, err := jc.datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return nil, 0, fmt.Errorf("加载数据集 %d 失败: %w", datasetID, err)
	}

	questions, err := jc.pipeline.ListQuestions(ctx, datasetID)
	if err != nil {
		return nil, 0, fmt.Errorf("加载问题失败 dataset=%d: %w", datasetID, err)
	}
	if len(questions) == 0 {
		return nil, 0, fmt.Errorf("数据集 %d 没有问题，无法导出", datasetID)
	}

	sftByQuestion, err := loadSftRecords(ctx, jc, datasetID)
	if err != nil {
		return nil, 0, err
	}
	grpoByQuestion, err := loadGrpoPrompts(ctx, jc, datasetID)
	if err != nil {
		return nil, 0, err
	}

	reasoningRecords, err := jc.reasoning.List(ctx, datasetID)
	if err != nil {
		return nil, 0, fmt.Errorf("加载推理记录失败 dataset=%d: %w", datasetID, err)
	}
	reasoningByQuestion := make(map[int64]model.ReasoningRecord, len(reasoningRecords))
	for _, record := range reasoningRecords {
		reasoningByQuestion[record.QuestionID] = record
	}

	rewardRecords, err := jc.rewards.List(ctx, datasetID)
	if err != nil {
		return nil, 0, fmt.Errorf("加载打分记录失败 dataset=%d: %w", datasetID, err)
	}
	rewardByQuestion := make(map[int64]model.RewardRecord, len(rewardRecords))
	for _, record := range rewardRecords {
		rewardByQuestion[record.QuestionID] = record
	}

	minScore, hasMinScore := filterFloat(filters, "minRewardScore")
	difficultyFilter := filterString(filters, "difficulty")
	domainFilter := filterString(filters, "domainName")
	requireReward, _ := filters["requireReward"].(bool)

	records := make([]exporter.Record, 0, len(questions))
	skipped := 0

	for _, question := range questions {
		if difficultyFilter != "" && !strings.EqualFold(question.Difficulty, difficultyFilter) {
			continue
		}
		if domainFilter != "" && !strings.EqualFold(question.DomainName, domainFilter) {
			continue
		}

		record := exporter.Record{
			DatasetID:    datasetID,
			DatasetName:  dataset.Name,
			QuestionID:   question.ID,
			Question:     question.Content,
			Difficulty:   question.Difficulty,
			DomainName:   question.DomainName,
			RewardLevels: dataset.RewardLevels,
		}

		// SFT 记录是问题→思维链→答案的一等公民载体，优先采用。
		if sft, ok := sftByQuestion[question.ID]; ok && model.RecordStatusUsableForDownstream(sft.Status) {
			record.ChainOfThought = sft.ChainOfThought
			record.Answer = sft.Answer
		}
		if record.ChainOfThought == "" && record.Answer == "" {
			if reasoning, ok := reasoningByQuestion[question.ID]; ok && model.RecordStatusUsableForDownstream(reasoning.Status) {
				record.ChainOfThought = reasoning.Reasoning
				record.Answer = reasoning.AnswerSummary
			}
		}

		if prompt, ok := grpoByQuestion[question.ID]; ok {
			record.JudgePrompt = prompt.JudgePrompt
			if len(prompt.Levels) > 0 {
				record.RewardLevels = prompt.Levels
			}
		}

		if reward, ok := rewardByQuestion[question.ID]; ok && model.RecordStatusUsableForDownstream(reward.Status) {
			record.RewardScore = reward.Score
			record.HasReward = true
		}

		if requireReward && !record.HasReward {
			continue
		}
		if hasMinScore && (!record.HasReward || record.RewardScore < minScore) {
			continue
		}
		if !hasExportableContent(record, targetKind) {
			skipped++
			continue
		}

		records = append(records, record)
	}

	return records, skipped, nil
}

// hasExportableContent 判断一条记录是否具备该导出场景必需的内容。
//
// 未指定 targetKind（映射为空）时只要三类内容任一非空即可导出。
func hasExportableContent(record exporter.Record, targetKind string) bool {
	switch targetKind {
	case "grpo":
		return strings.TrimSpace(record.JudgePrompt) != ""
	case "sft":
		return strings.TrimSpace(record.ChainOfThought) != "" || strings.TrimSpace(record.Answer) != ""
	default:
		return strings.TrimSpace(record.ChainOfThought) != "" ||
			strings.TrimSpace(record.Answer) != "" ||
			strings.TrimSpace(record.JudgePrompt) != ""
	}
}

// loadSftRecords 读取 SFT 样本，按问题 ID 索引。
func loadSftRecords(ctx context.Context, jc *jobContext, datasetID int64) (map[int64]model.SftRecord, error) {
	rows, err := jc.db().Query(ctx, `
		SELECT id, dataset_id, question_id, domain_id, chain_of_thought, answer, chain_steps, status, created_at, updated_at
		FROM sft_records WHERE dataset_id = $1`, datasetID)
	if err != nil {
		return nil, fmt.Errorf("加载 SFT 样本失败 dataset=%d: %w", datasetID, err)
	}
	defer rows.Close()

	out := map[int64]model.SftRecord{}
	for rows.Next() {
		var item model.SftRecord
		var steps []byte
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.QuestionID, &item.DomainID,
			&item.ChainOfThought, &item.Answer, &steps, &item.Status,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if len(steps) > 0 {
			_ = json.Unmarshal(steps, &item.ChainSteps)
		}
		out[item.QuestionID] = item
	}
	return out, rows.Err()
}

// loadGrpoPrompts 读取教师评判提示词，按问题 ID 索引。
func loadGrpoPrompts(ctx context.Context, jc *jobContext, datasetID int64) (map[int64]model.GrpoPrompt, error) {
	rows, err := jc.db().Query(ctx, `
		SELECT id, dataset_id, question_id, domain_id, levels, judge_prompt, level_rubrics,
		       framework_ref, status, created_at, updated_at
		FROM grpo_prompts WHERE dataset_id = $1`, datasetID)
	if err != nil {
		return nil, fmt.Errorf("加载 GRPO 提示词失败 dataset=%d: %w", datasetID, err)
	}
	defer rows.Close()

	out := map[int64]model.GrpoPrompt{}
	for rows.Next() {
		var item model.GrpoPrompt
		var levels, rubrics []byte
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.QuestionID, &item.DomainID,
			&levels, &item.JudgePrompt, &rubrics, &item.FrameworkRef, &item.Status,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if len(levels) > 0 {
			_ = json.Unmarshal(levels, &item.Levels)
		}
		if len(rubrics) > 0 {
			_ = json.Unmarshal(rubrics, &item.LevelRubrics)
		}
		out[item.QuestionID] = item
	}
	return out, rows.Err()
}

// filterString 读取字符串型过滤条件。
func filterString(filters map[string]any, key string) string {
	value, ok := filters[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// filterFloat 读取数值型过滤条件。JSON 反序列化后数值统一是 float64。
func filterFloat(filters map[string]any, key string) (float64, bool) {
	value, ok := filters[key].(float64)
	return value, ok
}
