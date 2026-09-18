package main

import (
	"context"
	"fmt"
	"log"

	"github.com/1420970597/llm/internal/eval"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L9 lane 独占：评估运行 worker。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.9 节。
// jobType "eval.run" 由 routes_eval_runs.go 入队。
//
// 流程：读运行 -> 解析维度 -> 解析裁判（剔除生成者自评）-> 抽样 -> 落条目
// -> 逐条逐维度打分 -> 回写进度。
//
// 进度语义：scored_items 每评完一条递增一次并落库。前端靠它显示进度，
// 因此必须在每条之后写，而不是全部跑完才写一次——否则用户在整个评估期间
// 只看到 0，无法判断任务是否还活着。

const evalRunStage = "eval.run"

func init() {
	RegisterJobHandler(evalRunStage, handleEvalRun)
}

func handleEvalRun(ctx context.Context, jc *jobContext, job jobPayload) error {
	runs := store.NewEvalRunStore(jc.db())

	run, err := runs.ActiveRun(ctx, job.DatasetID)
	if err != nil {
		if store.IsEvalRunNotFound(err) {
			log.Printf("eval.run.no_active_run dataset=%d", job.DatasetID)
			return nil
		}
		return err
	}

	if err := executeEvalRun(ctx, jc, runs, run); err != nil {
		_ = runs.UpdateRunStatus(ctx, run.ID, "failed", run.TotalItems, run.ScoredItems, err.Error())
		return err
	}
	return nil
}

// evalSelectedJudgeIDs 取本次运行实际选定的裁判 provider id 集合。
//
// 来源是 L7 落库的 eval_run_judges 记录（而非 eval_runs.judge_provider_ids）：
// 前者带着剔除判定（生成者自评 / 缺 API Key），是启动接口校验过的权威集合。
func evalSelectedJudgeIDs(ctx context.Context, jc *jobContext, runID int64) (map[int64]struct{}, error) {
	records, err := store.NewEvalJudgeStore(jc.db()).ListRunJudges(ctx, runID)
	if err != nil {
		return nil, err
	}
	selected := make(map[int64]struct{}, len(records))
	for _, record := range records {
		if record.Excluded {
			continue
		}
		selected[record.ProviderID] = struct{}{}
	}
	return selected, nil
}

// filterEvalJudges 只保留 selected 里的裁判，保持 LoadJudgeRefs 的原有顺序。
func filterEvalJudges(candidates []eval.JudgeRef, selected map[int64]struct{}) []eval.JudgeRef {
	kept := make([]eval.JudgeRef, 0, len(candidates))
	for _, judge := range candidates {
		if _, ok := selected[judge.ProviderID]; !ok {
			continue
		}
		kept = append(kept, judge)
	}
	return kept
}

func executeEvalRun(ctx context.Context, jc *jobContext, runs *store.EvalRunStore, run model.EvalRun) error {
	// 与冻结契约一致：异步任务同时落 generation_runs，前端据此统一轮询进度。
	progress, err := jc.generationRuns.StartRun(ctx, run.DatasetID, evalRunStage, 1)
	if err != nil {
		return err
	}
	finish := func(status, summary string) {
		if err := jc.generationRuns.FinishRun(ctx, progress.ID, status, summary); err != nil {
			log.Printf("eval.run.finish_progress_failed run=%d err=%v", run.ID, err)
		}
	}

	// 1. 解析维度定义。维度是评分的标尺，没有它无法构造提示词。
	dimensions, err := store.NewEvalDimensionStore(jc.db()).ListByKeys(ctx, run.DimensionKeys)
	if err != nil {
		finish("failed", err.Error())
		return err
	}
	if len(dimensions) == 0 {
		err := fmt.Errorf("评估运行 %d 没有可用维度（dimension_keys=%v）", run.ID, run.DimensionKeys)
		_ = runs.UpdateRunStatus(ctx, run.ID, "failed", 0, 0, err.Error())
		finish("failed", err.Error())
		return err
	}

	// 2. 解析裁判。generatorProviderID 必须传入：生成该数据集的模型禁止自评，
	//    否则模型给自己的数据打高分，评估结论失去意义。
	candidates, _, err := eval.LoadJudgeRefs(ctx, jc.prompts, run.GeneratorProvider)
	if err != nil {
		finish("failed", err.Error())
		return err
	}

	// 2.1 收敛到「本次运行选定的裁判」。
	//     LoadJudgeRefs 返回的是环境里全部可用 provider，而用户在
	//     PUT /api/v1/eval/runs/{runId}/judges 里选定的是其中一部分。
	//     不过滤就会用用户没选的模型去打分：分数落库后无法分辨它来自哪个裁判，
	//     报告里的「本次评估用了哪些裁判」也与用户配置不符。
	selectedJudgeIDs, err := evalSelectedJudgeIDs(ctx, jc, run.ID)
	if err != nil {
		finish("failed", err.Error())
		return err
	}
	judges := filterEvalJudges(candidates, selectedJudgeIDs)
	if len(judges) == 0 {
		// 两种成因都如实报错，不伪造一份空报告：
		//   - 环境里只有生成者 provider（全部候选被自评规则剔除）
		//   - 运行选定的裁判已被停用 / 移出环境
		err := fmt.Errorf("评估运行 %d 没有可用裁判（已选定 %d 个，环境候选 %d 个）：生成者模型禁止自评，请确认裁判 provider 处于启用状态",
			run.ID, len(selectedJudgeIDs), len(candidates))
		_ = runs.UpdateRunStatus(ctx, run.ID, "failed", 0, 0, err.Error())
		finish("failed", err.Error())
		return err
	}

	// 3. 取数据源并抽样。
	sources, err := runs.LoadEvalSources(ctx, run.DatasetID)
	if err != nil {
		finish("failed", err.Error())
		return err
	}
	if len(sources) == 0 {
		// 没有可评估的数据不是失败：明确记录为完成 + 说明，避免前端一直转圈。
		_ = runs.UpdateRunStatus(ctx, run.ID, "completed", 0, 0, "该数据集没有可评估的数据")
		finish("completed", "")
		log.Printf("eval.run.empty dataset=%d", run.DatasetID)
		return nil
	}

	byQuestionID := make(map[int64]store.EvalSourceItem, len(sources))
	questionIDs := make([]int64, 0, len(sources))
	for _, source := range sources {
		byQuestionID[source.QuestionID] = source
		questionIDs = append(questionIDs, source.QuestionID)
	}

	selected, err := eval.SampleQuestionIDs(eval.SampleSpec{
		Mode:        run.SamplingMode,
		Ratio:       run.SampleRatio,
		Size:        run.SampleSize,
		Seed:        run.ID,
		QuestionIDs: questionIDs,
	})
	if err != nil {
		_ = runs.UpdateRunStatus(ctx, run.ID, "failed", 0, 0, err.Error())
		finish("failed", err.Error())
		return err
	}

	// 4. 落条目。ON CONFLICT DO NOTHING 使重放幂等。
	items := make([]store.EvalItemInput, 0, len(selected))
	for index, questionID := range selected {
		source := byQuestionID[questionID]
		items = append(items, store.EvalItemInput{
			QuestionID: questionID,
			ItemIndex:  index,
			Payload: map[string]any{
				"question":   source.Question,
				"reasoning":  source.Reasoning,
				"answer":     source.Answer,
				"difficulty": source.Difficulty,
				"domainId":   source.DomainID,
			},
		})
	}
	if _, err := runs.InsertItems(ctx, run.ID, run.DatasetID, items); err != nil {
		finish("failed", err.Error())
		return err
	}

	storedItems, err := runs.ListItems(ctx, run.ID, len(items)+1, 0)
	if err != nil {
		finish("failed", err.Error())
		return err
	}
	if err := runs.UpdateRunStatus(ctx, run.ID, "running", len(storedItems), 0, ""); err != nil {
		finish("failed", err.Error())
		return err
	}

	// 5. 逐条逐维度打分。
	scored := 0
	failedScores := 0
	judgeScored := map[int64]int{}
	judgeErrors := map[int64]string{}

	for _, item := range storedItems {
		question, _ := item.Payload["question"].(string)
		reasoning, _ := item.Payload["reasoning"].(string)
		answer, _ := item.Payload["answer"].(string)

		results := eval.ScoreItemDimensions(ctx, eval.ItemScoreRequest{
			Judges:     judges,
			Dimensions: dimensions,
			Question:   question,
			Reasoning:  reasoning,
			Answer:     answer,
			Timeout:    eval.DefaultJudgeTimeout,
		})

		inputs := make([]store.EvalScoreInput, 0, len(results))
		for _, result := range results {
			input := store.EvalScoreInput{
				EvalItemID:      item.ID,
				JudgeProviderID: result.JudgeProviderID,
				DimensionKey:    result.DimensionKey,
				Score:           result.Outcome.Score,
				Rationale:       result.Outcome.Rationale,
				RawResponse:     result.Outcome.Raw,
				Status:          result.Outcome.Status,
			}
			if result.Outcome.Status == eval.ScoreStatusFailed {
				failedScores++
				if result.Outcome.Err != nil {
					judgeErrors[result.JudgeProviderID] = result.Outcome.Err.Error()
				}
			} else {
				judgeScored[result.JudgeProviderID]++
			}
			inputs = append(inputs, input)
		}

		// 失败的评分也要落库：否则报告里看不出「这个维度为什么没有分数」，
		// 会被误读成漏评。
		if _, err := runs.UpsertScores(ctx, run.ID, inputs); err != nil {
			finish("failed", err.Error())
			return err
		}

		scored++
		// 每条之后回写进度：前端靠 scored_items 显示进度条。
		if err := runs.UpdateRunStatus(ctx, run.ID, "running", len(storedItems), scored, ""); err != nil {
			log.Printf("eval.run.progress_write_failed run=%d err=%v", run.ID, err)
		}
	}

	// 6. 回写每个裁判的进度，供前端展示「哪个裁判完成了多少」。
	for _, judge := range judges {
		status := "completed"
		summary := ""
		if judgeErrors[judge.ProviderID] != "" {
			status = "partial_failed"
			summary = judgeErrors[judge.ProviderID]
		}
		if err := store.NewEvalJudgeStore(jc.db()).UpdateJudgeStatus(
			ctx, run.ID, judge.ProviderID, status, judgeScored[judge.ProviderID], summary); err != nil {
			log.Printf("eval.run.judge_status_failed run=%d provider=%d err=%v", run.ID, judge.ProviderID, err)
		}
	}

	// 7. 汇总最终状态。
	finalStatus := "completed"
	errorSummary := ""
	if failedScores > 0 {
		if failedScores == scored*len(judges)*len(dimensions) {
			finalStatus = "failed"
			errorSummary = "全部维度打分失败"
		} else {
			// 部分失败必须如实标为 partial_failed，不能报成 completed——
			// 否则用户会以为报告覆盖了全部维度。
			finalStatus = "partial_failed"
			errorSummary = fmt.Sprintf("%d 次打分失败，报告基于其余成功评分", failedScores)
		}
	}
	if err := runs.UpdateRunStatus(ctx, run.ID, finalStatus, len(storedItems), scored, errorSummary); err != nil {
		finish("failed", err.Error())
		return err
	}
	finish(finalStatus, errorSummary)

	log.Printf("eval.run.done run=%d dataset=%d items=%d scored=%d failed_scores=%d status=%s",
		run.ID, run.DatasetID, len(storedItems), scored, failedScores, finalStatus)
	return nil
}
