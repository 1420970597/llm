package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 L1 方向生成的 worker 侧，核心是断点续跑。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.1 节。
//
// 续跑语义：逐领域处理，每完成一个领域就把该领域 id 写进 generation_runs.cursor。
// 续跑时只处理 cursor 里没有的领域，已完成的绝不重跑。

func init() {
	RegisterJobHandler("directions.generate", handleDirectionGenerationJob)
}

func handleDirectionGenerationJob(ctx context.Context, jc *jobContext, job jobPayload) error {
	return runDirectionGeneration(ctx, jc, job.DatasetID)
}

func runDirectionGeneration(ctx context.Context, jc *jobContext, datasetID int64) error {
	dataset, err := jc.datasets.GetDataset(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("加载数据集 %d 失败: %w", datasetID, err)
	}

	domains, err := jc.datasets.ListRootDomains(ctx, datasetID)
	if err != nil {
		return fmt.Errorf("列出领域失败: %w", err)
	}
	if len(domains) == 0 {
		return fmt.Errorf("数据集 %d 没有领域，无法生成方向", datasetID)
	}

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := jc.datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		return fmt.Errorf("解析 provider 失败: %w", err)
	}
	provider := llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
	}

	run, err := jc.generationRuns.ResumeTarget(ctx, datasetID, store.DirectionStage)
	if errors.Is(err, pgx.ErrNoRows) {
		// 队列里可能残留一条已被后续运行取代的旧 job（重试、或运行已完成
		// 后才被消费）。此时没有待续跑的记录，直接幂等返回，不能报错，
		// 否则会把已经完成的数据集状态改成 *_failed。
		log.Printf("directions skipped dataset=%d reason=no-resumable-run", datasetID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取方向生成运行记录失败: %w", err)
	}

	cursor, err := store.DecodeDirectionCursor(run.Cursor)
	if err != nil {
		return err
	}
	directionCount := cursor.DirectionCount
	if directionCount <= 0 {
		directionCount = dataset.DirectionCount
	}
	if directionCount <= 0 {
		directionCount = 3
	}

	// 断点续跑的核心：只处理未完成的领域。
	pending := store.PendingDomainIDs(domains, cursor.CompletedDomainIDs)
	if len(pending) == 0 {
		if err := jc.generationRuns.FinishRun(ctx, run.ID, "completed", ""); err != nil {
			return err
		}
		_ = jc.datasets.UpdateStatus(ctx, datasetID, "directions_completed")
		log.Printf("directions already complete dataset=%d produced=%d", datasetID, cursor.ProducedDirections)
		return nil
	}
	log.Printf("directions resume dataset=%d pending_domains=%d total_domains=%d m=%d",
		datasetID, len(pending), len(domains), directionCount)

	pendingSet := make(map[int64]struct{}, len(pending))
	for _, id := range pending {
		pendingSet[id] = struct{}{}
	}

	produced := cursor.ProducedDirections
	failedDomains := make([]int64, 0)
	var firstErr error

	for _, domain := range domains {
		if _, wanted := pendingSet[domain.ID]; !wanted {
			continue
		}

		existing, err := jc.datasets.ListDirectionsByParent(ctx, datasetID, domain.ID)
		if err != nil {
			return fmt.Errorf("读取领域 %d 的已有方向失败: %w", domain.ID, err)
		}
		existingNames := make([]string, 0, len(existing))
		for _, item := range existing {
			existingNames = append(existingNames, item.Name)
		}

		directions, err := llm.GenerateDirections(ctx, provider, llm.DirectionGenerateInput{
			Domain:   domain,
			Count:    directionCount,
			Existing: existingNames,
		})
		if err != nil {
			// 单个领域失败不终止整批：记入 failed 列表，让本轮结束后状态为 partial_failed，
			// 用户可以调用 resume 只重跑这些领域。
			log.Printf("directions domain failed dataset=%d domain_id=%d domain=%q err=%v",
				datasetID, domain.ID, domain.Name, err)
			failedDomains = append(failedDomains, domain.ID)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		inserted, err := jc.datasets.UpsertDirections(ctx, datasetID, directions)
		if err != nil {
			return fmt.Errorf("写入领域 %d 的方向失败: %w", domain.ID, err)
		}
		produced += len(inserted)

		cursor.CompletedDomainIDs = append(cursor.CompletedDomainIDs, domain.ID)
		cursor.ProducedDirections = produced
		cursor.DirectionCount = directionCount
		cursor.FailedDomainIDs = failedDomains

		if err := jc.generationRuns.SaveCursor(ctx, run.ID, store.EncodeDirectionCursor(cursor),
			len(cursor.CompletedDomainIDs), run.TotalUnits); err != nil {
			return fmt.Errorf("回写方向生成游标失败: %w", err)
		}
		log.Printf("directions domain done dataset=%d domain_id=%d inserted=%d produced=%d",
			datasetID, domain.ID, len(inserted), produced)
	}

	if len(failedDomains) > 0 {
		summary := fmt.Sprintf("%d/%d 个领域生成失败，可调用 generation-runs/directions/resume 续跑",
			len(failedDomains), len(pending))
		if err := jc.generationRuns.FinishRun(ctx, run.ID, "partial_failed", summary); err != nil {
			return err
		}
		_ = jc.datasets.UpdateStatus(ctx, datasetID, "directions_partial_failed")
		log.Printf("directions partial dataset=%d failed_domains=%d produced=%d", datasetID, len(failedDomains), produced)
		return fmt.Errorf("方向生成部分失败: %s", summary)
	}

	if err := jc.generationRuns.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		return err
	}
	_ = jc.datasets.UpdateStatus(ctx, datasetID, "directions_completed")
	log.Printf("directions completed dataset=%d produced=%d", datasetID, produced)
	return nil
}

// 保证 model 包被引用（方向生成结果类型来自 model.Domain）。
var _ = model.Domain{}
