package main

import (
	"context"
	"fmt"
	"log"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L4 lane 独占。通过 job 注册表接入，不修改 worker/main.go。

func init() {
	RegisterJobHandler("grpo.generate", handleGrpoGeneration)
}

// handleGrpoGeneration 为数据集中每个问题生成教师模型评判提示词。
//
// 单个问题失败不终止整批：记为该行 status=failed 并继续，
// 避免一个畸形问题让整个数据集的 GRPO 分支产出为空。
func handleGrpoGeneration(ctx context.Context, jc *jobContext, job jobPayload) error {
	datasetID := job.DatasetID

	dataset, err := jc.datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("load dataset %d: %w", datasetID, err)
	}

	levels := llm.NormalizeLevels(dataset.RewardLevels)
	if len(levels) < 2 {
		return fmt.Errorf("dataset %d has fewer than two reward levels: %v", datasetID, dataset.RewardLevels)
	}

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := jc.datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		return fmt.Errorf("resolve provider for dataset %d: %w", datasetID, err)
	}
	provider := llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
	}

	grpoStore := store.NewGrpoStore(jc.db())
	contexts, err := grpoStore.ListQuestionContexts(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("list question contexts for dataset %d: %w", datasetID, err)
	}
	if len(contexts) == 0 {
		log.Printf("grpo.generate.skip dataset_id=%d reason=no_questions", datasetID)
		return nil
	}

	prompts := make([]model.GrpoPrompt, 0, len(contexts))
	failed := 0
	for _, item := range contexts {
		log.Printf("grpo.generate.question.start dataset_id=%d question_id=%d levels=%v", datasetID, item.QuestionID, levels)
		output, err := llm.GenerateGrpoPrompt(ctx, provider, llm.GrpoPromptInput{
			RootKeyword:   dataset.RootKeyword,
			DirectionName: item.DirectionName,
			Question:      item.QuestionText,
			ChainSteps:    item.ChainSteps,
			Levels:        levels,
		})

		prompt := model.GrpoPrompt{
			DatasetID:  datasetID,
			QuestionID: item.QuestionID,
			DomainID:   item.DirectionID,
			Levels:     levels,
			Status:     "generated",
		}
		if err != nil {
			failed++
			prompt.Status = "failed"
			log.Printf("grpo.generate.question.error dataset_id=%d question_id=%d err=%v", datasetID, item.QuestionID, err)
		} else {
			prompt.JudgePrompt = output.JudgePrompt
			prompt.LevelRubrics = output.LevelRubrics
			prompt.FrameworkRef = output.FrameworkRef
			log.Printf("grpo.generate.question.done dataset_id=%d question_id=%d rubrics=%d prompt_chars=%d",
				datasetID, item.QuestionID, len(output.LevelRubrics), len(output.JudgePrompt))
		}
		prompts = append(prompts, prompt)
	}

	// failed 行不写库：judge_prompt 为空的行对前端与导出都没有价值，
	// 留着会让「已生成」计数虚高。失败详情已在日志与返回值中体现。
	persistable := make([]model.GrpoPrompt, 0, len(prompts))
	for _, prompt := range prompts {
		if prompt.Status == "failed" {
			continue
		}
		persistable = append(persistable, prompt)
	}
	if err := grpoStore.UpsertPrompts(ctx, datasetID, persistable); err != nil {
		return fmt.Errorf("persist grpo prompts for dataset %d: %w", datasetID, err)
	}

	log.Printf("grpo.generate.done dataset_id=%d generated=%d failed=%d levels=%v",
		datasetID, len(persistable), failed, levels)

	if failed > 0 && len(persistable) == 0 {
		return fmt.Errorf("all %d grpo prompts failed for dataset %d", failed, datasetID)
	}
	return nil
}
