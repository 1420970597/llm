package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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

	// **按批次的目标类型选择生成器**（T23）：SFT 与 GRPO 共用同一条
	// runner 生命周期（批次/单元/样本版本/恢复语义），只在「生成什么」上分叉。
	// 目标类型取自**批次记录**而不是项目当前值：项目类型在运行后不可切换，
	// 但把判断建立在「当前值」上是脆弱的（历史批次应始终按它当时的目标执行）。
	batch, err := env.Batches().GetBatch(ctx, *job.BatchID)
	if err != nil {
		return nil, err
	}
	var generator studio.UnitGenerator
	switch batch.TargetKind {
	case model.TargetKindGRPO:
		generator = &grpoUnitGenerator{datasets: datasets, documents: env.Documents(), sources: store.NewSourceChunkStore(env.Pool), usage: env.Usage(), projects: store.NewProjectStore(env.Pool), batches: env.Batches(), jobID: job.ID}
	case model.TargetKindSFT:
		generator = &sftUnitGenerator{datasets: datasets, documents: env.Documents(), sources: store.NewSourceChunkStore(env.Pool), usage: env.Usage(), projects: store.NewProjectStore(env.Pool), batches: env.Batches(), jobID: job.ID}
	default:
		// 未知目标类型必须显式失败：猜一个会让 GRPO 项目产出 SFT 结构的样本，
		// 而那种错误只在质量实验按错误量表打分时才暴露。
		return nil, fmt.Errorf("批次的目标类型 %q 不受支持，无法选择生成器", batch.TargetKind)
	}

	runner := &studio.BatchRunner{
		Batches:   env.Batches(),
		Documents: env.Documents(),
		Usage:     env.Usage(),
		Generator: generator,
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
	sources   sourceChunkReader
	usage     *store.UsageStore
	projects  *store.ProjectStore
	batches   *store.BatchStore
	jobID     int64
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
		MaxTokens:       request.GenerationConfig.MaxTokens,
		Temperature:     request.GenerationConfig.Temperature,
	})
	var receipts generationReceipts
	provider.ObserveResponse = receipts.observe
	provider.Accounting = &studioCallAccounting{usage: generator.usage, projects: generator.projects, batches: generator.batches,
		projectID: request.ProjectID, batchID: &request.BatchID, connectionID: connectionID,
		idempotencyKey: fmt.Sprintf("batch:%d:item:%s:attempt:%d", request.BatchID, request.ItemKey, request.Attempt),
		attempt:        request.Attempt, purpose: "batch_generate", provider: provider, jobID: generator.jobID}

	steps, err := generator.standardSteps(ctx, request)
	if err != nil {
		return studio.UnitResult{}, err
	}
	question, materials, err := questionFor(ctx, provider, request, generator.sources, generator.documents, steps)
	if err != nil {
		return studio.UnitResult{}, err
	}

	payload, err := llm.GenerateSft(ctx, provider, llm.SftInput{
		RootKeyword: request.Unit.DirectionName,
		Question: model.Question{
			Content:    question,
			Difficulty: request.Unit.Difficulty,
		},
		Steps:           steps,
		IncludeAnswer:   true,
		SourceMaterials: materials,
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
		Title:           question,
		Usage:           receipts.usage(),
		RequestID:       strings.Join(receipts.requestIDs, ","),
		ResponseModelID: receipts.modelID,
		SourceChunkIDs:  append([]int64(nil), request.Unit.SourceChunkIDs...),
		Payload: map[string]any{
			"question":   question,
			"reasoning":  reasoning,
			"answer":     strings.TrimSpace(payload.Answer),
			"source":     questionSource(request.Unit.Source),
			"difficulty": request.Unit.Difficulty,
		},
	}, nil
}

// studioCallAccounting accounts for every paid HTTP attempt before content is
// committed. A failed answer still leaves a receipt for its successful question.
type studioCallAccounting struct {
	usage          *store.UsageStore
	projects       *store.ProjectStore
	batches        *store.BatchStore
	projectID      int64
	batchID        *int64
	connectionID   int64
	idempotencyKey string
	attempt        int
	purpose        string
	provider       llm.ProviderConfig
	jobID          int64
	call           int
}

func (accounting *studioCallAccounting) ReserveCall(ctx context.Context, meta llm.RequestMetadata) (llm.CallSettlement, error) {
	if accounting.usage == nil || accounting.projects == nil || (accounting.batchID != nil && accounting.batches == nil) {
		return nil, fmt.Errorf("configuration: 缺少预算依赖，禁止未记账的模型调用")
	}
	if accounting.projectID <= 0 || accounting.connectionID <= 0 || accounting.attempt <= 0 || strings.TrimSpace(accounting.idempotencyKey) == "" || strings.TrimSpace(accounting.purpose) == "" {
		return nil, fmt.Errorf("configuration: 模型请求缺少可追溯的项目、连接或尝试身份")
	}
	project, err := accounting.projects.GetProject(ctx, accounting.projectID)
	if err != nil {
		return nil, err
	}
	batchLimit := int64(0)
	if accounting.batchID != nil {
		batch, err := accounting.batches.GetBatch(ctx, *accounting.batchID)
		if err != nil {
			return nil, err
		}
		if batch.ProjectID != project.ID {
			return nil, fmt.Errorf("configuration: 批次预算不属于本项目")
		}
		batchLimit = batch.Budget.LimitMinor
	}
	connectionID := accounting.connectionID
	price, found, err := accounting.usage.ResolvePriceVersion(ctx, connectionID, accounting.provider.Model, time.Now())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("configuration: 模型尚未配置价格版本，无法可靠预留预算")
	}
	fingerprint := model.EndpointFingerprint(accounting.provider.BaseURL)
	if price.EndpointFP != "" && price.EndpointFP != fingerprint {
		return nil, fmt.Errorf("configuration: 模型价格与当前接入点不一致，请更新价格版本")
	}
	quote := model.QuoteReservation(meta.InputTokensUpperBound, meta.MaxOutputTokens, price)
	if !quote.Ok {
		return nil, fmt.Errorf("configuration: %s", quote.Reason)
	}
	accounting.call++
	var jobID *int64
	if accounting.jobID > 0 {
		jobID = &accounting.jobID
	}
	entry, created, err := accounting.usage.ReserveUsage(ctx, store.ReserveUsageInput{
		ProjectID: accounting.projectID, BatchID: accounting.batchID, JobID: jobID, Attempt: accounting.attempt,
		Purpose: accounting.purpose, IdempotencyKey: fmt.Sprintf("%s:call:%d", accounting.idempotencyKey, accounting.call),
		ConnectionID: &connectionID, EndpointFP: fingerprint, ModelName: accounting.provider.Model,
		ConfigFingerprint: meta.ConfigFingerprint, Currency: price.Currency, PriceVersionID: &price.ID,
		PriceVersion: price.PriceVersion, ReservationMinor: quote.AmountMinor,
		ProjectLimitMinor: project.Budget.LimitMinor, BatchLimitMinor: batchLimit,
	})
	if err != nil {
		if errors.Is(err, store.ErrBatchPaused) {
			return nil, studio.ErrBatchPaused
		}
		if errors.Is(err, store.ErrBudgetExhausted) && accounting.batchID != nil && project.Budget.OnExhausted != model.BudgetOnExhaustedStop {
			if _, pauseErr := accounting.batches.PauseBatch(ctx, project.ID, *accounting.batchID, 0); pauseErr != nil && !errors.Is(pauseErr, store.ErrBatchNotControllable) {
				return nil, fmt.Errorf("configuration: 预算不足且暂停失败，未调用模型: %w", pauseErr)
			}
			return nil, fmt.Errorf("configuration: 预算不足，已暂停后续请求: %w", studio.ErrBatchPaused)
		}
		return nil, fmt.Errorf("configuration: 预算预留失败，未调用模型: %w", err)
	}
	if !created {
		return nil, fmt.Errorf("configuration: 本次模型请求已预留或结算，需恢复为新尝试，不能重复收费")
	}
	return func(settleCtx context.Context, response llm.ResponseMetadata, callErr error) error {
		errorClass := ""
		if callErr != nil {
			errorClass = classifyStudioJobError(callErr)
		}
		_, err := accounting.usage.SettleUsage(settleCtx, entry.ID, store.SettleUsageInput{
			Charge:    model.ComputeCharge(response.Usage, price, price.PriceVersion),
			RequestID: response.RequestID, ResponseModelID: response.ModelID, ErrorClass: errorClass,
		})
		if err != nil {
			return fmt.Errorf("configuration: 用量结算失败，保留预留待核对: %w", err)
		}
		return nil
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

type sourceChunkReader interface {
	GetChunks(context.Context, int64, []int64) ([]model.SourceChunk, error)
}

type sourceVersionReader interface {
	GetVersionByID(context.Context, int64) (store.DocumentVersion, error)
}

// questionFor is shared by SFT and GRPO. Missing evidence requires an explicit
// keyword-only source choice; an old unspecified source never silently degrades.
func questionFor(ctx context.Context, provider llm.ProviderConfig, request studio.UnitRequest,
	sources sourceChunkReader, documents sourceVersionReader, steps []model.ChainStep) (string, []llm.SourceMaterial, error) {
	var materials []llm.SourceMaterial
	switch request.Unit.Source {
	case model.SourceAI:
		if len(request.Unit.SourceChunkIDs) != 0 {
			return "", nil, fmt.Errorf("configuration: AI 方向不能混入素材块，请选择素材来源")
		}
	case model.SourceDocument:
		if len(request.Unit.SourceChunkIDs) == 0 || sources == nil || documents == nil || request.BlueprintVersionID == nil {
			return "", nil, fmt.Errorf("missing source: 请关联素材块并在蓝图生成节点选择已完成采集的素材版本")
		}
		blueprintVersion, err := documents.GetVersionByID(ctx, *request.BlueprintVersionID)
		if err != nil {
			return "", nil, err
		}
		if blueprintVersion.ProjectID != request.ProjectID || blueprintVersion.Kind != model.KindBlueprint {
			return "", nil, fmt.Errorf("configuration: 蓝图版本不属于本项目")
		}
		var blueprint model.BlueprintPayload
		if err := json.Unmarshal(blueprintVersion.Payload, &blueprint); err != nil {
			return "", nil, err
		}
		if blueprint.Nodes.Generation.SourceVersionID <= 0 {
			return "", nil, fmt.Errorf("missing source version: 蓝图未冻结素材版本，请保存蓝图后新建批次")
		}
		version, err := documents.GetVersionByID(ctx, blueprint.Nodes.Generation.SourceVersionID)
		if err != nil {
			return "", nil, err
		}
		if version.ProjectID != request.ProjectID || version.Kind != model.KindSource {
			return "", nil, fmt.Errorf("configuration: 素材版本不属于本项目")
		}
		var source model.SourcePayload
		if err := json.Unmarshal(version.Payload, &source); err != nil {
			return "", nil, err
		}
		allowed := make(map[int64]bool)
		for _, document := range source.Documents {
			for _, id := range document.ChunkIDs {
				allowed[id] = true
			}
		}
		for _, id := range request.Unit.SourceChunkIDs {
			if !allowed[id] {
				return "", nil, fmt.Errorf("configuration: 素材块 %d 不在冻结的素材版本中，请采用完整素材版本后新建批次", id)
			}
		}
		chunks, err := sources.GetChunks(ctx, request.ProjectID, request.Unit.SourceChunkIDs)
		if err != nil {
			var fields model.FieldErrors
			if errors.As(err, &fields) {
				return "", nil, fmt.Errorf("configuration: %w", err)
			}
			return "", nil, err
		}
		if len(chunks) != len(request.Unit.SourceChunkIDs) {
			return "", nil, fmt.Errorf("missing source chunk: 素材被擦除或不存在，请重新关联后新建批次")
		}
		totalRunes := 0
		for _, chunk := range chunks {
			if strings.TrimSpace(chunk.Content) == "" {
				return "", nil, fmt.Errorf("missing source content: 素材正文已被擦除")
			}
			totalRunes += len([]rune(chunk.Content))
			materials = append(materials, llm.SourceMaterial{ID: chunk.ID, HeadingPath: chunk.HeadingPath, Content: chunk.Content})
		}
		if totalRunes > 32000 {
			return "", nil, fmt.Errorf("configuration: 关联素材超过 32000 字符，请缩小该方向素材范围")
		}
	default:
		return "", nil, fmt.Errorf("missing source: 方向缺少素材；请关联素材或显式选择「AI 按关键词生成」")
	}
	direction := request.Unit.DirectionName
	if direction == "" {
		direction = request.Unit.DirectionStableID
	}
	difficulty, _ := llm.ReconcileDifficulty(request.Unit.Difficulty)
	questions, err := llm.GenerateQuestionsV2(ctx, provider, llm.QuestionGenInput{
		RootKeyword: request.Unit.DomainName, QuestionsPerDirection: 1,
		DifficultyMix:   map[string]float64{difficulty: 1},
		Directions:      []llm.DirectionContext{{DomainName: direction, ChainSteps: steps}},
		SourceMaterials: materials, UnitOrdinal: request.Unit.Ordinal,
	})
	if err != nil {
		return "", nil, err
	}
	if len(questions) != 1 || strings.Contains(questions[0].Content, "：第 ") {
		return "", nil, fmt.Errorf("empty output: 模型未生成可用问题，拒绝编号占位内容")
	}
	return questions[0].Content, materials, nil
}

func questionSource(source string) string {
	if source == model.SourceDocument {
		return "document"
	}
	return "keyword_only"
}

type generationReceipts struct {
	input, output           int64
	inputKnown, outputKnown bool
	count                   int
	requestIDs              []string
	modelID                 string
}

func (receipts *generationReceipts) observe(meta llm.ResponseMetadata) {
	if receipts.count == 0 {
		receipts.inputKnown, receipts.outputKnown = true, true
	}
	receipts.count++
	if meta.Usage.InputTokens == nil {
		receipts.inputKnown = false
	} else {
		receipts.input += *meta.Usage.InputTokens
	}
	if meta.Usage.OutputTokens == nil {
		receipts.outputKnown = false
	} else {
		receipts.output += *meta.Usage.OutputTokens
	}
	if meta.RequestID != "" {
		receipts.requestIDs = append(receipts.requestIDs, meta.RequestID)
	}
	if meta.ModelID != "" {
		receipts.modelID = meta.ModelID
	}
}

func (receipts *generationReceipts) usage() model.TokenUsage {
	usage := model.TokenUsage{Source: model.UsageSourceUnknown}
	if receipts.count > 0 && receipts.inputKnown {
		value := receipts.input
		usage.InputTokens = &value
	}
	if receipts.count > 0 && receipts.outputKnown {
		value := receipts.output
		usage.OutputTokens = &value
	}
	if usage.HasAnyToken() {
		usage.Source = model.UsageSourceProvider
	}
	return usage
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
