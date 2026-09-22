package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件是 GRPO 的**生成适配**（Issue #160 T23）。
//
// 契约：docs/plans/atelier-implementation.md §2.2（GRPO payload 字段）、
// §5（蓝图节点）；#160 T23 的原文要求。
//
// 与 SFT 分支的关系（T23 明确要求「内容和 SFT 分支明确」）：
//
//	两者共用**同一生命周期**（batch runner / batch_items / sample_versions /
//	恢复与暂停语义），只在「生成什么」上分叉：
//	  SFT  → question + reasoning + answer
//	  GRPO → question + judge_prompt + levels + level_rubrics
//
// 因此 GRPO 的 payload **不包含** answer/reasoning：
// 把判据转写成 SFT 答案（或反过来用 reward_records 冒充教师材料）会让
// 两种训练数据混在一起，而下游无法区分 —— 那正是 T23 禁止的事。

// grpoBlueprintLevelsKey 是蓝图生成节点里档位配置的键名。
//
// 为什么放在 `jsonSchema`（自由字段）而不是新增 typed 字段：
// `BlueprintGenerationNode` 属于 T04 冻结的 schema，新增字段需要改冻结契约；
// 而 `jsonSchema` 本来就是「输出字段约束」的自由载体，档位列表正是
// GRPO 的输出结构约束（levels 数组）。
//
// 读不到档位时**报错**而不是退化到某个默认档位：默认档位会让「用户没配」
// 静默变成「系统替他决定了判分标准」，而判分标准是 GRPO 数据的核心语义。
const grpoBlueprintLevelsKey = "levels"

// grpoUnitGenerator 是 GRPO 的 UnitGenerator 实现。
//
// 依赖**具体 store** 而不是自造窄接口：窄接口的返回类型（例如版本行的形态）
// 必须与具体 store 完全一致才有意义，否则要写一层适配器 ——
// 而那层适配器只是把同一个类型搬来搬去（初版就是这么写的，编译器当场指出
// 「指向接口的指针不实现接口」与「窄接口不匹配具体类型」）。与 SFT 生成器
// 保持同一种构造方式，减少两处分叉。
type grpoUnitGenerator struct {
	datasets  *store.DatasetStore
	documents *store.DocumentStore
}

func (generator *grpoUnitGenerator) GenerateUnit(ctx context.Context, request studio.UnitRequest) (studio.UnitResult, error) {
	if request.CoverageVersionID == nil && request.BlueprintVersionID == nil {
		return studio.UnitResult{}, fmt.Errorf("missing blueprint: GRPO 生成必须引用蓝图版本（档位与判据来自它）")
	}

	// 档位与教师模板版本来自**冻结的蓝图版本**（不是「当前采用」）。
	levels, frameworkRef, err := generator.loadBlueprintLevels(ctx, request)
	if err != nil {
		return studio.UnitResult{}, err
	}

	connectionID := request.GenerationConfig.ModelConnectionID
	if connectionID <= 0 {
		return studio.UnitResult{}, fmt.Errorf("missing model connection: 批次快照没有模型连接，请到设计页的生成节点补齐")
	}
	baseURL, modelName, providerType, reasoningEffort, apiKey, err := generator.datasets.ResolveProvider(ctx, connectionID)
	if err != nil {
		// 连接被停用时**不**回退到默认连接（T07 验收项「撤销连接时不偷偷换模型」）。
		return studio.UnitResult{}, fmt.Errorf("model connection unavailable: %w", err)
	}

	steps, err := grpoStandardSteps(ctx, generator.documents, request)
	if err != nil {
		return studio.UnitResult{}, err
	}

	output, err := llm.GenerateGrpoPrompt(ctx, llm.WithUsageReporting(llm.ProviderConfig{
		BaseURL: baseURL, Model: modelName, ProviderType: providerType,
		ReasoningEffort: reasoningEffort, APIKey: apiKey,
	}), llm.GrpoPromptInput{
		RootKeyword:   request.Unit.DomainName,
		DirectionName: request.Unit.DirectionName,
		Question:      grpoQuestionFor(request),
		ChainSteps:    steps,
		Levels:        levels,
	})
	if err != nil {
		return studio.UnitResult{}, err
	}

	payload, err := buildGrpoPayload(grpoQuestionFor(request), output, levels)
	if err != nil {
		// 生成的判据不合规（缺档/重复档/空规则）必须**显式失败**：
		// 静默写入一个不合规的样本会让质量实验按错误的量表打分，
		// 而那时已经花了两次模型调用。
		return studio.UnitResult{}, err
	}
	_ = frameworkRef
	return studio.UnitResult{
		Title:   grpoQuestionFor(request),
		Payload: payload,
	}, nil
}

// buildGrpoPayload 组装 grpo.sample.v1 的 payload（**纯函数**，可单测）。
//
// 三条结构约束（T23 验收项「schema 校验覆盖缺档、重复档、空规则、坏 JSON」）：
//
//  1. **levels 保留数组**（不 join 成逗号字符串）：下游 GRPO 训练要按档位
//     一一对应，逗号字符串会让档位边界无法还原（§2.2 明确禁止 Join）；
//  2. **level_rubrics 保留对象数组**，且与 levels 一一对应；
//  3. **payload 里没有 answer/reasoning**：GRPO 的内容是「问题 + 判据」，
//     把 SFT 的答案字段塞进来会让两种数据混在一起（T23 明确禁止）。
//
// 组装后立刻用 T05 的 schema 校验自检：让不合规的产物在**写入之前**失败。
func buildGrpoPayload(question string, output llm.GrpoPromptOutput, levels []string) (map[string]any, error) {
	if strings.TrimSpace(question) == "" {
		return nil, fmt.Errorf("empty output: GRPO 样本没有问题内容")
	}
	if len(levels) < 2 {
		return nil, fmt.Errorf("levels 至少需要两档，实际 %d 档", len(levels))
	}

	rubrics := make([]map[string]any, 0, len(output.LevelRubrics))
	for _, rubric := range output.LevelRubrics {
		rubrics = append(rubrics, map[string]any{
			"level":      rubric.Level,
			"criteria":   rubric.Criteria,
			"acceptCase": rubric.AcceptCase,
			"rejectCase": rubric.RejectCase,
		})
	}

	payload := map[string]any{
		"question":     question,
		"judgePrompt":  strings.TrimSpace(output.JudgePrompt),
		"levels":       levels,
		"levelRubrics": rubrics,
		// 框架来源：说明这份判据基于哪种思维框架生成，
		// 使「同类判据是否可比」有依据（T23 要求保留 provenance）。
		"frameworkRef": strings.TrimSpace(output.FrameworkRef),
	}

	// 自检：用与落库路径**同一份**校验（T05 的 schema），
	// 因此「生成器认为合规、落库时被拒」不可能发生。
	if err := validateGrpoPayloadForWrite(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// validateGrpoPayloadForWrite 用 T05 的 schema 校验 payload。
func validateGrpoPayloadForWrite(payload map[string]any) error {
	levelsRaw, found := payload["levels"].([]string)
	if !found {
		return fmt.Errorf("levels 必须是字符串数组（不得为其它形态）")
	}
	rubricsRaw, found := payload["levelRubrics"].([]map[string]any)
	if !found {
		return fmt.Errorf("levelRubrics 必须是对象数组")
	}
	rubrics := make([]model.GrpoLevelRubric, 0, len(rubricsRaw))
	for _, item := range rubricsRaw {
		rubric := model.GrpoLevelRubric{}
		if value, ok := item["level"].(string); ok {
			rubric.Level = value
		}
		if value, ok := item["criteria"].(string); ok {
			rubric.Criteria = value
		}
		if value, ok := item["acceptCase"].(string); ok {
			rubric.AcceptCase = value
		}
		if value, ok := item["rejectCase"].(string); ok {
			rubric.RejectCase = value
		}
		rubrics = append(rubrics, rubric)
	}
	if err := model.ValidateGRPOSamplePayload(levelsRaw, rubrics); err != nil {
		return fmt.Errorf("生成的 GRPO 判据不合规：%w", err)
	}
	if strings.TrimSpace(payload["judgePrompt"].(string)) == "" {
		return fmt.Errorf("empty output: 没有生成裁判提示词")
	}
	return nil
}

// loadBlueprintLevels 从冻结的蓝图版本读档位与框架来源。
func (generator *grpoUnitGenerator) loadBlueprintLevels(ctx context.Context, request studio.UnitRequest) ([]string, string, error) {
	if request.BlueprintVersionID == nil {
		return nil, "", fmt.Errorf("missing blueprint: GRPO 生成必须引用蓝图版本")
	}
	version, err := generator.documents.GetVersionByID(ctx, *request.BlueprintVersionID)
	if err != nil {
		return nil, "", fmt.Errorf("read blueprint version: %w", err)
	}
	var payload model.BlueprintPayload
	if err := json.Unmarshal(version.Payload, &payload); err != nil {
		return nil, "", fmt.Errorf("blueprint version payload invalid: %w", err)
	}

	// 档位来自生成节点的输出结构约束（见 grpoBlueprintLevelsKey 的说明）。
	raw, found := payload.Nodes.Generation.JSONSchema[grpoBlueprintLevelsKey]
	if !found {
		return nil, "", fmt.Errorf(
			"missing levels: 蓝图生成节点没有配置档位（%s）；GRPO 至少需要两档，请在设计页补齐后新建批次",
			grpoBlueprintLevelsKey)
	}
	levels := make([]string, 0, 4)
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				levels = append(levels, text)
			}
		}
	case []string:
		levels = append(levels, typed...)
	default:
		return nil, "", fmt.Errorf("levels 必须是字符串数组，蓝图里的实际形态是 %T", raw)
	}

	// 归一化（去空白/去空值/去重）后仍须至少两档：重复档位是 T23 明确要挡的。
	normalized := llm.NormalizeLevels(levels)
	if len(normalized) < 2 {
		return nil, "", fmt.Errorf(
			"levels 去重后不足两档（原始 %d 档）：档位重复或为空都会让判分标准失效", len(levels))
	}
	return normalized, payload.Nodes.Generation.ModelVersion, nil
}

// grpoQuestionFor 生成该单元的问题文本（与 SFT 分支同一形态）。
func grpoQuestionFor(request studio.UnitRequest) string {
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

// grpoStandardSteps 读取被引用标准版本的步骤（与 SFT 共用同一适配）。
func grpoStandardSteps(ctx context.Context, documents *store.DocumentStore, request studio.UnitRequest) ([]model.ChainStep, error) {
	if request.StandardVersionID == nil {
		return nil, nil
	}
	version, err := documents.GetVersionByID(ctx, *request.StandardVersionID)
	if err != nil {
		return nil, fmt.Errorf("read standard version: %w", err)
	}
	var payload model.StandardPayload
	if err := json.Unmarshal(version.Payload, &payload); err != nil {
		return nil, fmt.Errorf("standard version payload invalid: %w", err)
	}
	return toChainSteps(payload.Steps), nil
}
