package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/store"
)

// questionsJobType 是问题生成的 job 类型。
const questionsJobType = "questions.generate"

// questionsStage 与 defaultQuestionsPerDirection 与 apps/api 共用同一份定义，
// 避免两个 package main 之间漂移。
const (
	questionsStage               = store.QuestionsStage
	defaultQuestionsPerDirection = store.DefaultQuestionsPerDirection
	questionsCursorKey           = store.QuestionsCursorMixKey
)

func init() {
	RegisterJobHandler(questionsJobType, handleQuestionsV2)
}

// handleQuestionsV2 是 v2 问题生成任务的处理入口。
//
// 覆盖 legacy 的 handleQuestionGeneration：按方向（level=2 domain）而非领域
// 生成，注入长链思维标准步骤，按难度配比分层，并做内容级去重。
func handleQuestionsV2(ctx context.Context, jc *jobContext, job jobPayload) error {
	datasetID := job.DatasetID
	questionStore := store.NewQuestionStoreV2(jc.db())

	dataset, err := jc.datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("load dataset %d: %w", datasetID, err)
	}

	directions, err := questionStore.ListDirections(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("list directions for dataset %d: %w", datasetID, err)
	}
	if len(directions) == 0 {
		log.Printf("questions.v2.skip dataset_id=%d reason=no_directions", datasetID)
		return nil
	}

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := jc.datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		return fmt.Errorf("resolve provider %d: %w", dataset.ProviderID, err)
	}

	perDirection := dataset.QuestionsPerDirect
	if perDirection <= 0 {
		perDirection = defaultQuestionsPerDirection
	}

	difficultyMix, err := loadDifficultyMix(ctx, jc, datasetID)
	if err != nil {
		return err
	}

	systemPrompt, userPrompt := loadQuestionPrompts(ctx, jc)

	input := llm.QuestionGenInput{
		DatasetID:             datasetID,
		RootKeyword:           dataset.RootKeyword,
		QuestionsPerDirection: perDirection,
		DifficultyMix:         difficultyMix,
		Directions:            toDirectionContexts(directions),
		SystemPrompt:          systemPrompt,
		UserPrompt:            userPrompt,
	}

	questions, err := llm.GenerateQuestionsV2(ctx, llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
	}, input)
	if err != nil {
		markQuestionsRunFailed(ctx, jc, datasetID, err)
		return err
	}

	result, err := questionStore.InsertQuestions(ctx, datasetID, questions)
	if err != nil {
		markQuestionsRunFailed(ctx, jc, datasetID, err)
		return err
	}

	total, err := questionStore.CountQuestions(ctx, datasetID)
	if err != nil {
		return err
	}
	markQuestionsRunDone(ctx, jc, datasetID, result, total, len(directions))

	if err := jc.datasets.UpdateStatus(ctx, datasetID, "questions_generated"); err != nil {
		log.Printf("questions.v2.status_update_failed dataset_id=%d err=%v", datasetID, err)
	}
	log.Printf("questions.v2.done dataset_id=%d directions=%d generated=%d inserted=%d skipped=%d total=%d",
		datasetID, len(directions), len(questions), result.Inserted, result.Skipped, total)
	return nil
}

// toDirectionContexts 把 store 层方向行转换成生成器输入。
func toDirectionContexts(rows []store.DirectionRow) []llm.DirectionContext {
	contexts := make([]llm.DirectionContext, 0, len(rows))
	for _, row := range rows {
		contexts = append(contexts, llm.DirectionContext{
			DomainID:   row.DomainID,
			DomainName: row.DomainName,
			ChainSteps: row.ChainSteps,
		})
	}
	return contexts
}

// loadDifficultyMix 从 generation_runs.cursor 读回入队时写入的难度配比。
// 读不到时返回 nil，生成器会使用默认配比。
func loadDifficultyMix(ctx context.Context, jc *jobContext, datasetID int64) (map[string]float64, error) {
	run, err := jc.generationRuns.ResumeTarget(ctx, datasetID, questionsStage)
	if err != nil {
		// 没有运行记录不是错误：任务可能由其他入口（如直接 LPUSH）投递。
		log.Printf("questions.v2.cursor.missing dataset_id=%d err=%v", datasetID, err)
		return nil, nil
	}
	raw, ok := run.Cursor[questionsCursorKey]
	if !ok || raw == nil {
		return nil, nil
	}

	switch typed := raw.(type) {
	case map[string]any:
		mix := map[string]float64{}
		for level, value := range typed {
			if weight, ok := toFloat(value); ok {
				mix[level] = weight
			}
		}
		if len(mix) == 0 {
			return nil, nil
		}
		return mix, nil
	case string:
		var mix map[string]float64
		if err := json.Unmarshal([]byte(typed), &mix); err != nil {
			log.Printf("questions.v2.cursor.decode_failed dataset_id=%d err=%v", datasetID, err)
			return nil, nil
		}
		return mix, nil
	default:
		return nil, nil
	}
}

// toFloat 把 JSON 解码得到的数值转成 float64。
func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// loadQuestionPrompts 读取管理端配置的问题生成提示词模板。
// 未配置时返回空串，生成器使用内置提示词。
func loadQuestionPrompts(ctx context.Context, jc *jobContext) (string, string) {
	template, err := jc.prompts.GetActivePromptByStage(ctx, "question-generation")
	if err != nil {
		return "", ""
	}
	return template.SystemPrompt, template.UserPrompt
}

// markQuestionsRunDone 更新运行记录的完成进度与游标。
func markQuestionsRunDone(ctx context.Context, jc *jobContext, datasetID int64, result store.InsertQuestionsResult, total, directions int) {
	run, err := jc.generationRuns.ResumeTarget(ctx, datasetID, questionsStage)
	if err != nil {
		log.Printf("questions.v2.run.done_lookup_failed dataset_id=%d err=%v", datasetID, err)
		return
	}
	cursor := run.Cursor
	if cursor == nil {
		cursor = map[string]any{}
	}
	cursor["inserted"] = result.Inserted
	cursor["skipped"] = result.Skipped
	cursor["totalQuestions"] = total
	cursor["directions"] = directions

	if err := jc.generationRuns.SaveCursor(ctx, run.ID, cursor, run.TotalUnits, run.TotalUnits); err != nil {
		log.Printf("questions.v2.run.save_cursor_failed dataset_id=%d err=%v", datasetID, err)
		return
	}
	status := "completed"
	if result.Inserted == 0 && result.Skipped > 0 {
		// 全部内容都已存在：不是失败，但明确标记以便排查。
		status = "completed"
	}
	if err := jc.generationRuns.FinishRun(ctx, run.ID, status, ""); err != nil {
		log.Printf("questions.v2.run.finish_failed dataset_id=%d err=%v", datasetID, err)
	}
}

// markQuestionsRunFailed 把运行记录标记为失败，保留游标供断点续跑。
func markQuestionsRunFailed(ctx context.Context, jc *jobContext, datasetID int64, cause error) {
	run, err := jc.generationRuns.ResumeTarget(ctx, datasetID, questionsStage)
	if err != nil {
		log.Printf("questions.v2.run.failed_lookup_failed dataset_id=%d err=%v", datasetID, err)
		return
	}
	if err := jc.generationRuns.FinishRun(ctx, run.ID, "partial_failed", cause.Error()); err != nil {
		log.Printf("questions.v2.run.mark_failed dataset_id=%d err=%v", datasetID, err)
	}
}
