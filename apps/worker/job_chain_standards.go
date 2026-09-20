package main

import (
	"context"
	"fmt"
	"log"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件实现 L2 的 worker 侧：为数据集下的每个方向生成长链思维标准步骤。

func init() {
	RegisterJobHandler("chain-standards.generate", handleChainStandardGeneration)
}

func handleChainStandardGeneration(ctx context.Context, jc *jobContext, job jobPayload) error {
	dataset, err := jc.datasets.GetDataset(ctx, job.DatasetID)
	if err != nil {
		return err
	}

	targets, err := chainStandardTargets(ctx, jc.datasets, job.DatasetID)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		log.Printf("chain-standards.generate.no_domains dataset=%d", job.DatasetID)
		return nil
	}

	// 读取入队时写入的 domainIds（enqueueJob 载荷固定为 {type,datasetId}，
	// 参数经 generation_runs.cursor 传递），据此只处理用户指定的方向。
	selected, err := selectedDomainIDs(ctx, jc, job.DatasetID)
	if err != nil {
		return err
	}
	allTargets := targets
	targets = pickDomains(targets, selected)
	if len(targets) == 0 {
		// cursor 里的选择集来自入队那一刻。数据重排后（例如 L1 重跑，原来的
		// level=2 方向被替换成 level=1 领域）可能与当前候选全部失配。此时若
		// 直接返回，任务会「成功但什么都没做」—— 调用方拿到 completed，用户
		// 却一条标准步骤都没有。选择集只是入队时的优化提示，不是权威：失配
		// 就回退为全量，让任务真的干活。
		log.Printf("chain-standards.generate.selection_stale dataset=%d selected=%d fallback=all",
			job.DatasetID, len(selected))
		targets = allTargets
	}
	// 之后的 cursor 只记录本次真正处理的方向，避免把过期 ID 一路带下去。
	effective := domainIDsOf(targets)

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := jc.datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		return err
	}
	provider := llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
	}

	chainStore := store.NewChainStandardStore(jc.db())

	// 断点续跑：跳过 cursor 中已完成的方向。
	run, err := jc.generationRuns.ResumeTarget(ctx, job.DatasetID, "chain-standards.generate")
	if err != nil {
		return err
	}
	done := doneDomainIDs(run.Cursor)

	failures := 0
	completed := len(done)
	for _, domain := range targets {
		if _, skip := done[domain.ID]; skip {
			continue
		}

		steps, err := llm.GenerateChainStandard(ctx, provider, llm.ChainStandardInput{
			RootKeyword:   dataset.RootKeyword,
			DirectionName: domain.Name,
		})
		if err != nil {
			failures++
			log.Printf("chain-standards.generate.domain_error dataset=%d domain_id=%d err=%v", job.DatasetID, domain.ID, err)
			continue
		}

		if _, err := chainStore.UpsertFromAI(ctx, job.DatasetID, domain.ID, store.DirectionKeyOf(domain.Name), steps); err != nil {
			failures++
			log.Printf("chain-standards.generate.persist_error dataset=%d domain_id=%d err=%v", job.DatasetID, domain.ID, err)
			continue
		}

		done[domain.ID] = struct{}{}
		completed++
		if err := jc.generationRuns.SaveCursor(ctx, run.ID, map[string]any{
			"domainIds":      effective,
			"doneDomainIds":  domainIDList(done),
			"lastDomainName": domain.Name,
		}, completed, len(targets)); err != nil {
			return err
		}
		log.Printf("chain-standards.generate.domain_done dataset=%d domain_id=%d steps=%d", job.DatasetID, domain.ID, len(steps))
	}

	status := "completed"
	summary := ""
	if failures > 0 {
		status = "partial_failed"
		summary = fmt.Sprintf("%d/%d 个方向生成失败", failures, len(targets))
	}
	if err := jc.generationRuns.FinishRun(ctx, run.ID, status, summary); err != nil {
		return err
	}

	// 推进 datasets.status（issue #139）。FinishRun 只更新 generation_runs，
	// 不会碰 datasets.status；此前本处理器漏了这一步，于是长链标准步骤已全部落库、
	// run 也是 completed，而数据集永远停在 `chain_standards_queued`。
	//
	// 目标是 `directions_completed`：长链标准步骤与方向生成同属「领域整理」阶段
	//（见 internal/store/dataset_store.go 的 queuedStageByStatus 注释与 rank 1 设定），
	// 完成后该阶段即就绪，用户可以进入问题生成。
	if err := jc.datasets.UpdateStatus(ctx, job.DatasetID, "directions_completed"); err != nil {
		return err
	}
	if failures > 0 {
		return fmt.Errorf("chain standards generation partially failed: %s", summary)
	}
	log.Printf("chain-standards.generate.done dataset=%d directions=%d", job.DatasetID, completed)
	return nil
}

// chainStandardTargets 返回需要生成长链标准步骤的方向。
// 优先取 level=2 的方向（L1 生成）；若尚无方向，则退回 level=1 领域，
// 使本 lane 在 L1 尚未合并时依然可用。
func chainStandardTargets(ctx context.Context, datasets *store.DatasetStore, datasetID int64) ([]model.Domain, error) {
	directions, err := datasets.ListDomainsByLevel(ctx, datasetID, 2)
	if err != nil {
		return nil, err
	}
	if len(directions) > 0 {
		return directions, nil
	}
	return datasets.ListDomainsByLevel(ctx, datasetID, 1)
}

// selectedDomainIDs 从 generation_runs.cursor 读取入队时指定的方向 ID。
func selectedDomainIDs(ctx context.Context, jc *jobContext, datasetID int64) ([]int64, error) {
	run, err := jc.generationRuns.ResumeTarget(ctx, datasetID, "chain-standards.generate")
	if err != nil {
		return nil, err
	}
	return cursorInt64List(run.Cursor, "domainIds"), nil
}

// pickDomains 按选中的 ID 过滤方向；selected 为空表示全选。
func pickDomains(domains []model.Domain, selected []int64) []model.Domain {
	if len(selected) == 0 {
		return domains
	}
	wanted := make(map[int64]struct{}, len(selected))
	for _, id := range selected {
		wanted[id] = struct{}{}
	}
	filtered := make([]model.Domain, 0, len(selected))
	for _, domain := range domains {
		if _, ok := wanted[domain.ID]; ok {
			filtered = append(filtered, domain)
		}
	}
	return filtered
}

// doneDomainIDs 读取 cursor 中已完成的方向集合。
func doneDomainIDs(cursor map[string]any) map[int64]struct{} {
	done := map[int64]struct{}{}
	for _, id := range cursorInt64List(cursor, "doneDomainIds") {
		done[id] = struct{}{}
	}
	return done
}

// domainIDList 把集合转为稳定的 ID 列表（按升序，便于断言与比对）。
func domainIDList(done map[int64]struct{}) []int64 {
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

// domainIDsOf 抽取方向 ID 列表，保持输入顺序。
func domainIDsOf(domains []model.Domain) []int64 {
	ids := make([]int64, 0, len(domains))
	for _, domain := range domains {
		ids = append(ids, domain.ID)
	}
	return ids
}

// cursorInt64List 从 cursor 中读取整型 ID 列表，兼容 JSON 解码后的 float64。
func cursorInt64List(cursor map[string]any, key string) []int64 {
	if cursor == nil {
		return nil
	}
	raw, ok := cursor[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case float64:
			ids = append(ids, int64(value))
		case int64:
			ids = append(ids, value)
		case int:
			ids = append(ids, int64(value))
		}
	}
	return ids
}
