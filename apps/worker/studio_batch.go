package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件把 T12 的批原生 runner 接到 Studio 作业系统上（Issue #160 T12）。
//
// 契约：docs/plans/atelier-implementation.md §4.2、§6.1；
// #160 T12 的落点 `apps/worker/registry.go`、`apps/worker/studio_jobs.go`。
//
// 与旧 `job_sft.go` 的关系（#160 T12 明确要求「旧 handler 继续通过适配维持行为」）：
// 旧 handler 按 `{type,datasetId}` 与 (dataset_id, question_id) upsert 工作，
// **本轮不动它**；新 runner 只读批次快照并追加样本版本。两条路径并存，
// 靠 `studio.` 前缀与独立队列隔离（T06 已保证不会互相误吞消息）。

func init() {
	RegisterStudioJobHandler(model.JobKindBatchGenerate, handleStudioBatchGenerate)
}

// handleStudioBatchGenerate 执行一个生成批次。
//
// 作业的 batchId 由 API 在创建批次时同事务写入（T08），因此这里不需要
// 再从 payload 里找 —— 从权威字段读比从 JSON 里解析更不容易错。
func handleStudioBatchGenerate(ctx context.Context, env *StudioJobEnv, job model.Job) (any, error) {
	if job.BatchID == nil {
		// 批次作业没有 batchId 说明创建路径有 bug。这是**配置/契约**问题，
		// 不是瞬时故障，重试不会变好（T07 的错误分类把 config_error 标为不可重试）。
		return nil, fmt.Errorf("批次作业缺少 batchId（作业创建路径异常）")
	}

	datasets := env.datasetStore()
	if datasets == nil {
		return nil, fmt.Errorf("worker 未注入 provider 解析依赖，无法执行批次生成")
	}

	runner := &studio.BatchRunner{
		Batches:   env.Batches(),
		Documents: env.Documents(),
		Usage:     env.Usage(),
		Generator: &sftUnitGenerator{datasets: datasets, documents: env.Documents()},
	}
	result, err := runner.RunBatch(ctx, *job.BatchID)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// sftUnitGenerator 是 UnitGenerator 的真实实现：调用 SFT 生成器。
//
// 三条与契约直接相关的行为：
//
//  1. **凭证每次从加密配置解析**（§2.4「凭证单独取，不能冻结明文密钥」）：
//     ModelConnectionID 来自批次快照（非秘密标识），而 API key 是这一行
//     现取现用，不落库、不进日志、不进 job payload。
//  2. **标准步骤来自被引用的标准版本**（不是「当前采用」）：这正是
//     「已有成功内容不受标准变更影响」的实现方式。
//  3. **要求供应商回传用量**（WithUsageReporting）：没有 usage 时费用只能记
//     未知，而未知会让预算只能按预留占用（T07）。
type sftUnitGenerator struct {
	datasets  *store.DatasetStore
	documents *store.DocumentStore
}

func (generator *sftUnitGenerator) GenerateUnit(ctx context.Context, request studio.UnitRequest) (studio.UnitResult, error) {
	connectionID := request.GenerationConfig.ModelConnectionID
	if connectionID <= 0 {
		return studio.UnitResult{}, fmt.Errorf("missing model connection: 批次快照没有模型连接，请到设计页的生成节点补齐")
	}

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := generator.datasets.ResolveProvider(ctx, connectionID)
	if err != nil {
		// 连接被删除/停用时**不**回退到默认连接：那会让用户在不知情的情况下
		// 用另一个（可能更贵或质量不同的）模型跑完一批（T07 验收项
		//「撤销连接时不偷偷换模型」）。
		return studio.UnitResult{}, fmt.Errorf("model connection unavailable: %w", err)
	}

	provider := llm.WithUsageReporting(llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
	})

	steps, err := generator.standardSteps(ctx, request)
	if err != nil {
		return studio.UnitResult{}, err
	}

	payload, err := llm.GenerateSft(ctx, provider, llm.SftInput{
		RootKeyword: request.Unit.DirectionName,
		Question: model.Question{
			Content:    questionFor(request),
			Difficulty: request.Unit.Difficulty,
		},
		Steps:         steps,
		IncludeAnswer: true,
	})
	if err != nil {
		return studio.UnitResult{}, err
	}

	// **字段名映射**：旧生成器返回 `chainOfThought`，而新契约要求 `reasoning`
	//（§2.2「旧 chainOfThought 命名不再作为新契约字段」）。这里显式做一次适配，
	// 而不是把旧名直接透传 —— 透传会让 store 的 schema 校验拒绝它（那是好事：
	// 拒绝比静默接受一个不在契约里的字段更能暴露问题）。
	reasoning := strings.TrimSpace(payload.ChainOfThought)
	if reasoning == "" {
		return studio.UnitResult{}, fmt.Errorf("empty output: 模型没有返回推理过程")
	}
	return studio.UnitResult{
		Title: questionFor(request),
		Payload: map[string]any{
			"question":  questionFor(request),
			"reasoning": reasoning,
			"answer":    strings.TrimSpace(payload.Answer),
		},
	}, nil
}

// standardSteps 读取被引用标准版本的步骤。
func (generator *sftUnitGenerator) standardSteps(ctx context.Context, request studio.UnitRequest) ([]model.ChainStep, error) {
	if request.StandardVersionID == nil {
		// 没有标准版本不是错误：试制可以在没有标准的情况下先跑通链路，
		// 而执行前的「必填」检查（T11 的 CheckNodeRequirements）由命令层负责。
		return nil, nil
	}
	version, err := generator.documents.GetVersionByID(ctx, *request.StandardVersionID)
	if err != nil {
		return nil, fmt.Errorf("read standard version: %w", err)
	}
	var payload model.StandardPayload
	if err := json.Unmarshal(version.Payload, &payload); err != nil {
		return nil, fmt.Errorf("standard version payload invalid: %w", err)
	}
	return toChainSteps(payload.Steps), nil
}

// toChainSteps 把新契约的 StandardStep 适配成旧生成器要的 ChainStep。
//
// 为什么需要适配而不是统一类型：`ChainStep` 是**第一轮冻结契约**里的类型
// （旧标准表与旧生成器都在用），`StandardStep` 是本轮 §5 的 typed schema
// （带稳定 ID、显式 order）。改任一方都会动到已冻结的接口，
// 因此这里做一次**显式**转换 —— 并且按 order 排序，因为新 schema 的
// 步骤顺序是数据（`order`），而旧类型用数组下标当顺序。
//
// 顺序很关键：标准步骤的顺序会被执行侧沿用（生成时按步推理）。
func toChainSteps(steps []model.StandardStep) []model.ChainStep {
	ordered := make([]model.StandardStep, len(steps))
	copy(ordered, steps)
	sort.SliceStable(ordered, func(i, j int) bool {
		// order 相同（含都为 0）时保持原数组顺序：稳定排序保证
		// 「没填 order 的步骤」不会被打乱。
		return ordered[i].Order < ordered[j].Order
	})

	converted := make([]model.ChainStep, 0, len(ordered))
	for index, step := range ordered {
		converted = append(converted, model.ChainStep{
			// Index 从 1 开始：旧类型用它做展示编号（「第 1 步」），
			// 而从 0 开始会在提示词里出现「第 0 步」。
			Index:       index + 1,
			Title:       step.Title,
			Description: step.Detail,
			Checkpoint:  step.Checkpoint,
		})
	}
	return converted
}

// questionFor 生成该单元的问题文本。
//
// 目前是确定性的模板：一个单元对应一个方向下的第 N 个问题。
// 真正的「问题内容生成」是 T12 后续与 T13 的规划阶段要接的（问题文本会
// 作为覆盖分配的一部分冻结进快照），因此这里刻意不调模型 —— 编造一个
// 看起来像模型输出的问题是更糟的选择。
func questionFor(request studio.UnitRequest) string {
	direction := request.Unit.DirectionName
	if direction == "" {
		direction = request.Unit.DirectionStableID
	}
	difficulty := request.Unit.Difficulty
	if difficulty == "" {
		difficulty = "normal"
	}
	return fmt.Sprintf("%s（难度 %s）：第 %d 题", direction, difficulty, request.Unit.Ordinal)
}

// ---------------------------------------------------------------------------
// StudioJobEnv 的依赖访问器
// ---------------------------------------------------------------------------
//
// 用「惰性构造 + 挂在 extras 上」而不是给 StudioJobEnv 加字段：
// 后者会让每个后续任务都要改这个结构体（以及所有构造点），
// 而 extras 是 T06 就留好的扩展点。

func (env *StudioJobEnv) Batches() *store.BatchStore {
	if value, ok := env.Extra("batchStore"); ok {
		if batches, ok := value.(*store.BatchStore); ok {
			return batches
		}
	}
	batches := store.NewBatchStore(env.Pool)
	env.SetExtra("batchStore", batches)
	return batches
}

func (env *StudioJobEnv) Documents() *store.DocumentStore {
	if value, ok := env.Extra("documentStore"); ok {
		if documents, ok := value.(*store.DocumentStore); ok {
			return documents
		}
	}
	documents := store.NewDocumentStore(env.Pool)
	env.SetExtra("documentStore", documents)
	return documents
}

func (env *StudioJobEnv) Usage() *store.UsageStore {
	if value, ok := env.Extra("usageStore"); ok {
		if usage, ok := value.(*store.UsageStore); ok {
			return usage
		}
	}
	usage := store.NewUsageStore(env.Pool)
	env.SetExtra("usageStore", usage)
	return usage
}

// datasetStore 返回带解密能力的 DatasetStore（用于解析 provider 凭证）。
//
// 返回 nil 表示 worker 没有注入 secretBox：那时**不能**继续 ——
// 拿不到凭证却继续执行只会得到「provider 配置不完整」这种看似业务错误
// 而实为部署错误的失败，让排障方向跑偏。
func (env *StudioJobEnv) datasetStore() *store.DatasetStore {
	if value, ok := env.Extra("datasetStore"); ok {
		if datasets, ok := value.(*store.DatasetStore); ok {
			return datasets
		}
	}
	secretBox, ok := env.Extra("secretBox")
	if !ok {
		return nil
	}
	// 直接断言具体类型而不是包一层包装：多一个包装类型只会在
	// 「注入方改了类型」时多一处失败点，而没有带来表达能力。
	box, ok := secretBox.(*appcrypto.SecretBox)
	if !ok || box == nil {
		return nil
	}
	datasets := store.NewDatasetStore(env.Pool, box)
	env.SetExtra("datasetStore", datasets)
	return datasets
}
