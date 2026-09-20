package main

import (
	"context"
	"fmt"
	"log"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L5 lane 独占。通过 job 注册表接入，不修改 worker/main.go。

func init() {
	RegisterJobHandler("sft.generate", handleSftGeneration)
}

// handleSftGeneration 为数据集中每个问题生成 SFT 样本（思维链 + 可选答案）。
//
// 单个问题失败不终止整批：记为该行 status=failed 并继续，
// 避免一个畸形问题让整个数据集的 SFT 分支产出为空。
func handleSftGeneration(ctx context.Context, jc *jobContext, job jobPayload) error {
	datasetID := job.DatasetID

	dataset, err := jc.datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("load dataset %d: %w", datasetID, err)
	}

	includeAnswer, err := sftIncludeAnswer(ctx, jc, datasetID)
	if err != nil {
		return err
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

	var promptTemplate *model.PromptTemplate
	if template, templateErr := jc.prompts.GetActivePromptByStage(ctx, "sft-generation"); templateErr == nil {
		promptTemplate = &template
	}

	sftStore := store.NewSftStore(jc.db())
	contexts, err := sftStore.ListQuestionContexts(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("list question contexts for dataset %d: %w", datasetID, err)
	}
	if len(contexts) == 0 {
		log.Printf("sft.generate.skip dataset_id=%d reason=no_questions", datasetID)
		return nil
	}

	// 断点续跑：跳过 cursor 中已完成的问题。
	run, err := jc.generationRuns.ResumeTarget(ctx, datasetID, "sft.generate")
	if err != nil {
		return err
	}
	done := doneQuestionIDs(run.Cursor)

	records := make([]model.SftRecord, 0, len(contexts))
	failed := 0
	completed := len(done)
	for _, item := range contexts {
		if _, skip := done[item.QuestionID]; skip {
			continue
		}

		log.Printf("sft.generate.question.start dataset_id=%d question_id=%d steps=%d include_answer=%t",
			datasetID, item.QuestionID, len(item.ChainSteps), includeAnswer)

		output, err := llm.GenerateSft(ctx, provider, llm.SftInput{
			DatasetID:      datasetID,
			RootKeyword:    dataset.RootKeyword,
			Question:       model.Question{ID: item.QuestionID, Content: item.QuestionText},
			Steps:          item.ChainSteps,
			IncludeAnswer:  includeAnswer,
			PromptTemplate: promptTemplate,
		})

		steps := llm.NormalizeChainSteps(item.ChainSteps)
		record := model.SftRecord{
			DatasetID:  datasetID,
			QuestionID: item.QuestionID,
			DomainID:   item.DirectionID,
			ChainSteps: steps,
		}
		if err != nil {
			failed++
			record.Status = llm.SftStatus(0, len(steps), err)
			log.Printf("sft.generate.question.error dataset_id=%d question_id=%d err=%v", datasetID, item.QuestionID, err)
		} else {
			record.ChainOfThought = output.ChainOfThought
			record.Answer = output.Answer
			record.Status = llm.SftStatus(llm.CountAlignedSteps(output.ChainOfThought, steps), len(steps), nil)
			log.Printf("sft.generate.question.done dataset_id=%d question_id=%d cot_runes=%d answer_runes=%d aligned=%d/%d status=%s",
				datasetID, item.QuestionID, len([]rune(output.ChainOfThought)), len([]rune(output.Answer)),
				llm.CountAlignedSteps(output.ChainOfThought, steps), len(steps), record.Status)
		}
		records = append(records, record)

		// 逐条落库后再推进游标：若先写游标再写数据，进程中途挂掉会让
		// 游标声称已完成、而库里没有对应样本，断点续跑时直接跳过 → 数据丢失。
		// failed 行不落库：空思维链对导出没有价值，留着会让「已生成」计数虚高。
		if record.Status != "failed" {
			if err := sftStore.UpsertRecords(ctx, datasetID, []model.SftRecord{record}); err != nil {
				return fmt.Errorf("persist sft record for dataset %d question %d: %w", datasetID, item.QuestionID, err)
			}
		}

		done[item.QuestionID] = struct{}{}
		completed++
		if err := jc.generationRuns.SaveCursor(ctx, run.ID, map[string]any{
			"includeAnswer":   includeAnswer,
			"doneQuestionIds": questionIDList(done),
		}, completed, len(contexts)); err != nil {
			return err
		}
	}

	persisted := 0
	for _, record := range records {
		if record.Status != "failed" {
			persisted++
		}
	}

	status := "completed"
	summary := ""
	if failed > 0 {
		status = "partial_failed"
		summary = fmt.Sprintf("%d/%d 条 SFT 样本生成失败", failed, len(records))
	}
	if err := jc.generationRuns.FinishRun(ctx, run.ID, status, summary); err != nil {
		return err
	}

	// 推进 datasets.status（issue #139）。
	//
	// 为什么必须显式做：FinishRun 只更新 `generation_runs.status`，
	// **不会碰 datasets.status**。此前本处理器漏了这一步，于是 SFT 数据已经
	// 全部落库、generation_runs 也是 completed，而数据集永远停在 `sft_queued` ——
	// 界面显示「SFT 数据生成排队中」，用户既不能继续也不知道要不要重试。
	// 实测 dataset 77 停在该状态 23 小时（`sft_records` 已有 2 条数据）。
	//
	// 与 job_directions.go 的既有写法保持一致（它是唯一做对了这一步的处理器）。
	sftStatus := "sft_generated"
	if status == "partial_failed" {
		sftStatus = "sft_partial"
	}
	if err := jc.datasets.UpdateStatus(ctx, datasetID, sftStatus); err != nil {
		return err
	}

	log.Printf("sft.generate.done dataset_id=%d generated=%d failed=%d include_answer=%t",
		datasetID, persisted, failed, includeAnswer)

	if failed > 0 && persisted == 0 {
		return fmt.Errorf("all %d sft samples failed for dataset %d", failed, datasetID)
	}
	return nil
}

// sftIncludeAnswer 从 generation_runs.cursor 读取本次是否生成答案。
//
// 入队载荷固定为 {type, datasetId}（jobPayload 是冻结结构），因此
// 请求体参数经 cursor 传递。缺省为 true，与请求体的零值语义一致。
func sftIncludeAnswer(ctx context.Context, jc *jobContext, datasetID int64) (bool, error) {
	run, err := jc.generationRuns.ResumeTarget(ctx, datasetID, "sft.generate")
	if err != nil {
		return false, err
	}
	value, ok := run.Cursor["includeAnswer"]
	if !ok {
		return true, nil
	}
	flag, ok := value.(bool)
	if !ok {
		// cursor 经 JSONB 往返后布尔仍是 bool；出现其他类型说明写入方有误，
		// 此处按默认值处理而不是让整批任务失败。
		return true, nil
	}
	return flag, nil
}

// doneQuestionIDs 从游标中还原已完成的问题 ID 集合。
func doneQuestionIDs(cursor map[string]any) map[int64]struct{} {
	done := map[int64]struct{}{}
	raw, ok := cursor["doneQuestionIds"]
	if !ok {
		return done
	}
	list, ok := raw.([]any)
	if !ok {
		return done
	}
	for _, entry := range list {
		if number, ok := entry.(float64); ok {
			done[int64(number)] = struct{}{}
		}
	}
	return done
}

// questionIDList 把集合转为稳定的升序 ID 列表，便于断点续跑与断言比对。
func questionIDList(done map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(done))
	for id := range done {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}
