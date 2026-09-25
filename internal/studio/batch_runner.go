package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件是 Atelier 的**批原生生成 runner**（Issue #160 T12）。
//
// 契约：docs/plans/atelier-implementation.md §4.2（批次状态转换）、§2.4（暂停语义）、
// §2.6（进度按单位）；#160 T12 的验收项。
//
// 与旧阶段管道的根本区别（这是「新运行不再走 dataset 中心」的落地点）：
//
//	旧：任务按 dataset + stage 走，生成结果按 (dataset_id, question_id) **upsert**，
//	    于是「同题重跑」会覆盖历史内容，而「改了标准再跑」会静默改写已有产出。
//	新：runner **只读批次快照**（蓝图/覆盖/标准/映射的版本 ID + 行内 hash），
//	    产出按 batch/item 身份**追加**为新的 sample_version。已有成功内容
//	    不受默认 prompt / provider / 标准变更影响。
//
// 另一条不可让步的性质：**快照是行内 hash，不是「当前采用」指针**。
// 因此「保存新蓝图」不会改变一个已提交批次的执行配置（T05 已保证），
// 而 runner 也不需要再加一层「当时用的是哪一版」的判断。

// UnitGenerator 生成一个单元的样本内容。
//
// 抽成接口是为了让 T12 的验收项「可控 provider 覆盖成功、空输出、截断、
// 无效 JSON、429、超时、部分失败」可以被**逐一驱动**：真实 provider 无法
// 按需产生这七种结果，而在测试里真的去调外部模型既不稳定也不该花钱。
//
// 实现约束：必须尊重 ctx（暂停与租约丢失都要能及时停下），
// 并且**不得**自行写库 —— 内容落库由 runner 用带 batch/item 身份的提交完成。
type UnitGenerator interface {
	GenerateUnit(ctx context.Context, request UnitRequest) (UnitResult, error)
}

// UnitRequest 是一个单元的生成输入。
//
// 所有字段都来自**批次快照**（不是「当前采用」的配置）：
// 这正是「已有成功内容不受后续配置变更影响」的实现方式。
type UnitRequest struct {
	ProjectID  int64
	BatchID    int64
	TargetKind string

	// ItemKey 是单元的稳定键（覆盖分配算出），用于追溯与幂等。
	ItemKey string
	// Attempt 是本次尝试序号（从 1 开始），随批次恢复递增。
	Attempt int

	// 快照：版本 ID + 行内 hash。hash 用于追溯「这一批用的是哪一版内容」，
	// 而 ID 用于读取被引用的 typed payload。
	BlueprintVersionID   *int64
	BlueprintContentHash string
	CoverageVersionID    *int64
	CoverageContentHash  string
	StandardVersionID    *int64
	StandardContentHash  string

	// GenerationConfig 是批次内联的生成配置（非秘密标识）。
	GenerationConfig model.BatchGenerationConfig

	// Unit 是覆盖分配算出的具体单元内容（领域/方向与配额内的序号）。
	Unit PlannedUnit
}

// UnitResult 是一个单元的成功产出。
type UnitResult struct {
	// Payload 是 typed 样本内容（SFT 或 GRPO），由实现按 target_kind 组装。
	// 它会被 store 的提交路径按 schema 校验（T05），因此实现不必重复校验格式。
	Payload map[string]any
	// Title 是样本的人类可读标题。
	Title string
	// Usage 是这次调用的用量元数据（T07）。
	Usage model.TokenUsage
	// RequestID / ResponseModelID 是供应商回执，用于对账与「是否被换模型」取证。
	RequestID       string
	ResponseModelID string
	// ConfigFingerprint 是本次请求的配置指纹（提供则覆盖 runner 的默认计算）。
	ConfigFingerprint string
}

// PlannedUnit 是覆盖分配算出的一个单元。
type PlannedUnit struct {
	// DomainStableID / DirectionStableID 是覆盖版本里的**稳定 ID**
	// （不是数组下标）：删除草稿方向不会破坏已引用该版本的批次。
	DomainStableID    string
	DirectionStableID string
	DomainName        string
	DirectionName     string
	// Ordinal 是该方向内的第几个单元（从 1 开始）。
	Ordinal int
	// Quota 是该方向的配额（用于进度分列展示，不用于编造总体百分比）。
	Quota int
	// Difficulty 是配比算出的难度档（可为空）。
	Difficulty string
}

// ItemKey 返回单元的稳定键。
//
// 形如 `domain/direction#ordinal`：**确定性**，因此重新规划（恢复失败项、
// 重跑同一批次）会命中同一个 item，而不是新建一批 —— 那是「反复点击恢复
// 不重复成果」的地基（T05 的 UNIQUE(batch_id, item_key) 与之配套）。
func (unit PlannedUnit) ItemKey() string {
	return fmt.Sprintf("%s/%s#%d", unit.DomainStableID, unit.DirectionStableID, unit.Ordinal)
}

// BatchRunner 执行一个生成批次。
type BatchRunner struct {
	Batches   *store.BatchStore
	Documents *store.DocumentStore
	Usage     *store.UsageStore
	Generator UnitGenerator

	// LeaseDuration 是单次单元的最大等待时间。
	// 与作业租约同量级：单元超时后由作业层的租约/重试处理，runner 不自己重试
	//（否则会与作业层的 attempt 计数重复计费）。
	LeaseDuration time.Duration
}

// BatchRunResult 是一次批次执行的摘要，会被写回 jobs.payload 供 API 读模型使用。
type BatchRunResult struct {
	BatchID        int64 `json:"batchId"`
	PlannedUnits   int   `json:"plannedUnits"`
	CompletedUnits int   `json:"completedUnits"`
	FailedUnits    int   `json:"failedUnits"`
	// Superseded 表示「本次没有新提交，因为批次已被要求暂停」。
	// 与「完成」区分：暂停后批次仍在等待恢复，而完成是终态。
	Superseded bool `json:"superseded"`
}

// ErrBatchPaused 表示批次的 control_state 要求停止提交新请求。
//
// 它不是失败：§2.4 明确「pause 只阻止新提交；在途请求仍可完成并计费」。
// runner 遇到它时把作业**成功**结束（没有待办工作），而不是标记失败 ——
// 否则用户点一次暂停就会看到一条失败记录。
var ErrBatchPaused = errors.New("批次已请求暂停，不再提交新的生成请求")

// RunBatch 执行一个批次的全部待办单元。
//
// 五条语义（每条都对应 T12 的一个验收点）：
//
//  1. **只读快照**：所有生成参数来自 batch 快照，不查「当前采用」。
//  2. **暂停即停新提交**：每个单元提交前检查 control_state。
//  3. **单元级错误分类**：失败的单元带 errorClass 与可操作原因，
//     成功内容保留；聚合状态按 item 事实（store.RefreshBatchCounts）。
//  4. **恢复不重跑成功项**：只处理 status ∈ {pending, running} 的单元
//     （running 是「上次被中断」，失败项需显式恢复）。
//  5. **并发不串数据**：并发度来自**本批次**的 generation_config，
//     且每个 goroutine 只持有自己的单元与结果，不做跨单元的状态共享。
func (runner *BatchRunner) RunBatch(ctx context.Context, batchID int64) (BatchRunResult, error) {
	if runner.Generator == nil {
		return BatchRunResult{}, NewError(CodeUnavailable, "生成器未配置，无法执行批次")
	}
	batch, err := runner.Batches.GetBatch(ctx, batchID)
	if err != nil {
		return BatchRunResult{}, err
	}
	result := BatchRunResult{BatchID: batch.ID, PlannedUnits: batch.PlannedUnits}

	// 暂停检查放在规划**之前**：暂停语义是「不提交新请求」，
	// 而规划会把单元写入 batch_items —— 那不会调模型，但会让用户在暂停后
	// 看到一批「待执行」单元，从而以为系统还在推进。
	if batch.ControlState == model.BatchControlPauseRequested || batch.Status == model.BatchStatusPaused {
		result.Superseded = true
		result.CompletedUnits = batch.CompletedUnits
		result.FailedUnits = batch.FailedUnits
		return result, nil
	}

	units, err := runner.PlanUnits(ctx, batch)
	if err != nil {
		return BatchRunResult{}, err
	}
	if err := runner.ensureItems(ctx, batch, units); err != nil {
		return BatchRunResult{}, err
	}

	concurrency := batch.GenerationConfig.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > model.MaxGenerationConcurrency {
		concurrency = model.MaxGenerationConcurrency
	}

	for {
		// 每轮开始前重新读一次控制状态：用户在批次跑到一半时点暂停，
		// 必须尽快生效（剩余单元不再提交），而不是等整批跑完。
		current, err := runner.Batches.GetBatch(ctx, batch.ID)
		if err != nil {
			return result, err
		}
		if current.ControlState == model.BatchControlPauseRequested {
			result.Superseded = true
			break
		}

		items, err := runner.Batches.ListBatchItems(ctx, batch.ID, model.ItemStatusPending, concurrency)
		if err != nil {
			return result, err
		}
		if len(items) == 0 {
			break
		}

		var waitGroup sync.WaitGroup
		for _, item := range items {
			if err := runner.runItem(ctx, batch, item); err != nil {
				// 单个单元的失败已经在 runItem 内落库（带 errorClass），
				// 这里只记录并继续 —— 一个单元失败不该让整批停摆。
				if errors.Is(err, ErrBatchPaused) {
					result.Superseded = true
					break
				}
			}
		}
		waitGroup.Wait()
	}

	refreshed, err := runner.Batches.RefreshBatchCounts(ctx, batch.ID)
	if err != nil {
		return result, err
	}
	result.CompletedUnits = refreshed.CompletedUnits
	result.FailedUnits = refreshed.FailedUnits
	result.PlannedUnits = refreshed.PlannedUnits

	// 终态事件：让批次详情的时间线能解释「为什么结束了」。
	//
	// issue #190：只有「计划量与完成量相等」才算完成。两者不等时发
	// BatchPartialFailed 并在载荷里带上缺口，这样详情页能直接回答
	// 「为什么不是已完成」——而不是让用户自己拿两个数字相减。
	if !result.Superseded {
		shortfall := refreshed.PlannedUnits - refreshed.CompletedUnits
		eventType := model.BatchEventCompleted
		detail := map[string]any{
			"projectId": batch.ProjectID,
			"batchId":   batch.ID,
			"completed": refreshed.CompletedUnits,
			"failed":    refreshed.FailedUnits,
		}
		if shortfall > 0 {
			eventType = model.BatchEventPartialFailed
			detail["planned"] = refreshed.PlannedUnits
			detail["shortfall"] = shortfall
			detail["reason"] = "产出少于计划量，已终止为部分完成，缺口必须显式处理"
		}
		if err := runner.Batches.AppendBatchEvent(ctx, batch.ID, batch.ProjectID,
			eventType, 0, detail); err != nil {
			return result, err
		}
	}
	return result, nil
}

// ensureItems 把计划单元写进 batch_items（幂等）。
func (runner *BatchRunner) ensureItems(ctx context.Context, batch model.Batch, units []PlannedUnit) error {
	for _, unit := range units {
		if _, _, err := runner.Batches.EnsureBatchItem(ctx, batch.ID, batch.ProjectID, unit.ItemKey(), nil); err != nil {
			return err
		}
	}
	return nil
}

// runItem 处理一个单元：抢占 → 生成 → 落库。
//
// 刻意**不做**单元级重试：重试由作业层按 attempt/退避/上限统一管理。
// 在 runner 内部再重试会与作业层的 attempt 计数重复，于是同一笔费用
// 会被算两次机会，而 max_attempts 也就失去意义。
func (runner *BatchRunner) runItem(ctx context.Context, batch model.Batch, item model.BatchItem) error {
	claimed, ok, err := runner.Batches.ClaimBatchItemForAttempt(ctx, item.ID)
	if err != nil {
		return err
	}
	if !ok {
		// 已被别的 worker 抢占（同批次可能被两个作业实例处理）：
		// 静默跳过，这是**正常**现象而不是错误 —— 抢占是 CAS。
		return nil
	}

	unit, err := runner.unitForItem(ctx, batch, claimed.ItemKey)
	if err != nil {
		return runner.failItem(ctx, batch, claimed, model.ErrorClassConfig, err.Error(), false)
	}

	request := UnitRequest{
		ProjectID:            batch.ProjectID,
		BatchID:              batch.ID,
		TargetKind:           batch.TargetKind,
		ItemKey:              claimed.ItemKey,
		Attempt:              claimed.Attempt,
		BlueprintVersionID:   optionalVersionID(batch.Snapshot.BlueprintVersionID),
		BlueprintContentHash: batch.Snapshot.BlueprintContentHash,
		CoverageVersionID:    optionalVersionID(batch.Snapshot.CoverageVersionID),
		CoverageContentHash:  batch.Snapshot.CoverageContentHash,
		StandardVersionID:    optionalVersionID(batch.Snapshot.StandardVersionID),
		StandardContentHash:  batch.Snapshot.StandardContentHash,
		GenerationConfig:     batch.GenerationConfig,
		Unit:                 unit,
	}

	generated, err := runner.Generator.GenerateUnit(ctx, request)
	if err != nil {
		return runner.failItem(ctx, batch, claimed, classifyUnitError(err), err.Error(), true)
	}
	if len(generated.Payload) == 0 {
		return runner.failItem(ctx, batch, claimed, model.ErrorClassEmptyOutput,
			"生成结果为空白，未写入样本版本", true)
	}

	// 提交成功内容：带上**batch/item 身份**，因此「同题不同批次的输出」
	// 与「原样本重生成」都是新版本而不是覆盖（T05 的样例版本只追加）。
	generatorConfig, err := json.Marshal(map[string]any{
		"itemKey":           claimed.ItemKey,
		"modelConnectionId": batch.GenerationConfig.ModelConnectionID,
		"modelVersion":      batch.GenerationConfig.ModelVersion,
		"concurrency":       batch.GenerationConfig.Concurrency,
		"maxTokens":         batch.GenerationConfig.MaxTokens,
		"responseModelId":   generated.ResponseModelID,
	})
	if err != nil {
		return runner.failItem(ctx, batch, claimed, model.ErrorClassInternal, err.Error(), false)
	}

	_, committed, err := runner.Batches.CommitBatchItemSuccess(ctx, batch.ID, batch.ProjectID, claimed.ID,
		store.AppendSampleVersionInput{
			ProjectID:            batch.ProjectID,
			SampleKey:            claimed.ItemKey,
			TargetKind:           batch.TargetKind,
			Title:                firstNonEmptyString(generated.Title, claimed.ItemKey),
			BatchID:              &batch.ID,
			BatchItemID:          &claimed.ID,
			Attempt:              claimed.Attempt,
			Payload:              generated.Payload,
			GeneratorConfig:      generatorConfig,
			StandardVersionID:    optionalVersionID(batch.Snapshot.StandardVersionID),
			StandardContentHash:  batch.Snapshot.StandardContentHash,
			BlueprintVersionID:   optionalVersionID(batch.Snapshot.BlueprintVersionID),
			BlueprintContentHash: batch.Snapshot.BlueprintContentHash,
		})
	if err != nil {
		return err
	}
	if !committed {
		// 该单元已经成功过（重放）：不重复计费，也不追加第二个版本。
		return nil
	}
	return nil
}

// failItem 记录单元失败（带错误类别与可操作原因）。
func (runner *BatchRunner) failItem(ctx context.Context, batch model.Batch, item model.BatchItem,
	errorClass, message string, retryable bool) error {
	if !model.IsRetryableJobError(errorClass) {
		// 不可重试的错误（schema/config）必须显式标成不可重试：
		// 否则「恢复失败项」会把它们再跑一遍并再花一次钱，
		// 而同样的输入必然得到同样的失败。
		retryable = false
	}
	if _, err := runner.Batches.CommitBatchItemFailure(ctx, batch.ID, item.ID, errorClass, message, retryable); err != nil {
		return err
	}
	if err := runner.Batches.AppendBatchEvent(ctx, batch.ID, batch.ProjectID,
		model.BatchEventPartialFailed, 0, map[string]any{
			"batchId":    batch.ID,
			"itemId":     item.ID,
			"errorClass": errorClass,
			"retryable":  retryable,
		}); err != nil {
		return err
	}
	return nil
}

// unitForItem 按 item_key 反查该单元（恢复时单元不重新分配）。
//
// 恢复路径必须用它而不是重新分配：重新分配会让「上次跑到第 7 个」变成
// 「从头再跑一遍」，那既会重复计费，也会产生重复内容。
func (runner *BatchRunner) unitForItem(ctx context.Context, batch model.Batch, itemKey string) (PlannedUnit, error) {
	units, err := runner.PlanUnits(ctx, batch)
	if err != nil {
		return PlannedUnit{}, err
	}
	for _, unit := range units {
		if unit.ItemKey() == itemKey {
			return unit, nil
		}
	}
	return PlannedUnit{}, fmt.Errorf("单元键 %s 不在本批次的计划里（快照或覆盖版本可能已变）", itemKey)
}

// PlanUnits 按覆盖版本分配**确定性**的单元清单。
//
// 分配规则（刻意简单且可复现）：
//
//	按覆盖版本的领域/方向顺序展开，每个方向按 quota 取 ordinal，
//	直到批次计划单元数用尽。同方向的剩余配额在下一批（扩量）继续用。
//
// 为什么不用随机/轮询：单元的 item_key 是 `domain/direction#ordinal`，
// 必须**稳定**，否则「恢复失败项」会算出另一批键，从而重跑已完成的工作。
// 为什么按 quota 而不是平均分：quota 是用户在设计阶段明确表达的覆盖意图
// （§5「配额、难度配比、来源」），平均分会让界面上的覆盖矩阵与实际产出不符。
func (runner *BatchRunner) PlanUnits(ctx context.Context, batch model.Batch) ([]PlannedUnit, error) {
	if batch.Snapshot.CoverageVersionID == 0 {
		// 没有覆盖版本时退化成「纯按数量」的计划：仍然给出稳定键
		//（`unit/#n`），使 T13 的「手动扩量」在没有覆盖方案时也能跑。
		units := make([]PlannedUnit, 0, batch.PlannedUnits)
		for index := 1; index <= batch.PlannedUnits; index++ {
			units = append(units, PlannedUnit{
				DomainStableID: "unit", DirectionStableID: "default", Ordinal: index,
			})
		}
		return units, nil
	}

	version, err := runner.Documents.GetVersionByID(ctx, batch.Snapshot.CoverageVersionID)
	if err != nil {
		return nil, err
	}
	var coverage model.CoveragePayload
	if err := json.Unmarshal(version.Payload, &coverage); err != nil {
		return nil, NewError(CodeValidation, "覆盖版本内容无法解析，请重新保存覆盖方案")
	}
	return AllocateUnits(coverage, batch.PlannedUnits), nil
}

// AllocateUnits 是 PlanUnits 的**纯函数**部分（可测试、无 IO）。
//
// 分配本身由 model.AllocateCoverageUnits 完成（#190 之后它同时服务于
// 「保存/启动前的容量校验」与「运行期的实际分配」），本函数只负责补上
// 展示用的难度档。
//
// 顺序：先按领域顺序，再按方向顺序，再按方向内 ordinal。
// 每个方向的单元数取其 quota；quota ≤ 0 时视为 1（否则该方向在矩阵上
// 有名字但永远不产出，而用户无法从界面看出原因）。
func AllocateUnits(coverage model.CoveragePayload, plannedUnits int) []PlannedUnit {
	covered := model.AllocateCoverageUnits(coverage, plannedUnits)
	if len(covered) == 0 {
		return nil
	}
	ratioByDirection := make(map[string][]model.DifficultyRatio)
	for _, domain := range coverage.Domains {
		for _, direction := range domain.Directions {
			ratioByDirection[domain.StableID+"/"+direction.StableID] = direction.DifficultyRatios
		}
	}
	units := make([]PlannedUnit, 0, len(covered))
	for _, unit := range covered {
		units = append(units, PlannedUnit{
			DomainStableID:    unit.DomainStableID,
			DirectionStableID: unit.DirectionStableID,
			DomainName:        unit.DomainName,
			DirectionName:     unit.DirectionName,
			Ordinal:           unit.Ordinal,
			Quota:             unit.Quota,
			Difficulty:        difficultyFor(ratioByDirection[unit.DomainStableID+"/"+unit.DirectionStableID], unit.Ordinal, unit.Quota),
		})
	}
	return units
}

// difficultyFor 按配比与 ordinal 决定难度档。
//
// 用累计占比而不是对 ordinal 取模：取模会让「70% easy / 30% hard」在
// 小配额下永远得到同一个档（例如 quota=2 时总是 easy, hard），
// 而累计占比在小配额下也能得到与配比一致的比例。
func difficultyFor(ratios []model.DifficultyRatio, ordinal, quota int) string {
	if len(ratios) == 0 || quota <= 0 {
		return ""
	}
	ordered := make([]model.DifficultyRatio, len(ratios))
	copy(ordered, ratios)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Difficulty < ordered[j].Difficulty })

	position := float64(ordinal) / float64(quota)
	cumulative := 0.0
	for _, ratio := range ordered {
		cumulative += ratio.Ratio
		if position <= cumulative+1e-9 {
			return ratio.Difficulty
		}
	}
	return ordered[len(ordered)-1].Difficulty
}

// classifyUnitError 把生成错误映射到契约的错误类别（§4.1 的 errorClass）。
//
// 判据与 worker 的 classifyStudioJobError 保持一致：只在有**明确证据**时
// 给可重试类别。把「配置错误」误判成「限流」会让恢复无限重试并持续花钱；
// 把「限流」误判成「配置错误」只让用户多点一次恢复。
func classifyUnitError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.ErrorClassTimeout
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "429"), strings.Contains(message, "rate limit"),
		strings.Contains(message, "too many requests"):
		return model.ErrorClassRateLimited
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline exceeded"),
		strings.Contains(message, "timed out"):
		return model.ErrorClassTimeout
	case strings.Contains(message, "truncat"), strings.Contains(message, "max_tokens"),
		strings.Contains(message, "length limit"):
		return model.ErrorClassTruncated
	case strings.Contains(message, "json"), strings.Contains(message, "invalid character"),
		strings.Contains(message, "unmarshal"):
		return model.ErrorClassInvalidJSON
	case strings.Contains(message, "empty"), strings.Contains(message, "no choices"),
		strings.Contains(message, "no content"):
		return model.ErrorClassEmptyOutput
	case strings.Contains(message, "schema"), strings.Contains(message, "validation"):
		return model.ErrorClassSchema
	case strings.Contains(message, "configuration"), strings.Contains(message, "missing"):
		return model.ErrorClassConfig
	case strings.Contains(message, "502"), strings.Contains(message, "503"),
		strings.Contains(message, "504"), strings.Contains(message, "connection reset"):
		return model.ErrorClassProvider
	default:
		return model.ErrorClassInternal
	}
}

// optionalVersionID 把「未引用」的 0 转成 nil。
//
// 与 store.nullableVersionID 同一语义（0 表示未引用，而 DB 里是 NULL）：
// 这里存在的理由是 UnitRequest 与提交输入用的是指针类型，
// 而批次快照列在 Go 侧是 int64。两处各自转换一次而不是共享函数，
// 是因为它们分属不同包（store 的转换是 DB 边界，这里是领域模型边界）。
func optionalVersionID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	value := id
	return &value
}

// firstNonEmptyString 返回第一个非空字符串。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
