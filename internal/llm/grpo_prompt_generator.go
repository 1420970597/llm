package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// GrpoPromptInput 生成一个问题的教师模型评判提示词所需输入。
type GrpoPromptInput struct {
	RootKeyword   string
	DirectionName string
	Question      string
	ChainSteps    []model.ChainStep
	Levels        []string
}

// GrpoPromptOutput 生成结果。
type GrpoPromptOutput struct {
	JudgePrompt  string
	LevelRubrics []model.GrpoLevelRubric
	FrameworkRef string
}

// grpoRubricPayload 教师模型为产出评判判据而返回的 JSON 结构。
type grpoRubricPayload struct {
	SceneSummary string `json:"sceneSummary"`
	LevelRubrics []struct {
		Level      string `json:"level"`
		Label      string `json:"label"`
		Criteria   string `json:"criteria"`
		AcceptCase string `json:"acceptCase"`
		RejectCase string `json:"rejectCase"`
	} `json:"levelRubrics"`
}

// NormalizeLevels 清洗用户给定的打分档次：去空白、去空值、去重，并保留用户给定顺序。
// 顺序有意义（例如 -1 → 0 → 1 由低到高），因此不能排序。
func NormalizeLevels(levels []string) []string {
	normalized := make([]string, 0, len(levels))
	seen := map[string]struct{}{}
	for _, level := range levels {
		trimmed := strings.TrimSpace(level)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

// GenerateGrpoPrompt 为一个问题生成教师模型评判提示词。
//
// 职责划分（这是本函数的核心设计）：
//   - 教师模型负责**实质性内容**：每个档次的判据、典型应给分/不应给分情形、场景概括。
//   - 本函数负责**结构性框架**：角色设定、整体性思考框架、结合场景的判断要求、
//     以及强制的 JSON 输出格式。
//
// 这样即使模型返回的判据质量参差，产出给教师模型的提示词也必然包含全部必需段落，
// 不会因为模型漏写角色或漏写输出格式而得到一份不可用的提示词。
func GenerateGrpoPrompt(ctx context.Context, provider ProviderConfig, input GrpoPromptInput) (GrpoPromptOutput, error) {
	if provider.ProviderType == "mock" {
		return GrpoPromptOutput{}, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return GrpoPromptOutput{}, fmt.Errorf("real provider configuration is incomplete")
	}

	levels := NormalizeLevels(input.Levels)
	if len(levels) < 2 {
		return GrpoPromptOutput{}, fmt.Errorf("at least two reward levels are required, got %d", len(levels))
	}
	if strings.TrimSpace(input.Question) == "" {
		return GrpoPromptOutput{}, fmt.Errorf("question content is required")
	}

	rubrics, err := generateLevelRubrics(ctx, provider, input, levels)
	if err != nil {
		return GrpoPromptOutput{}, err
	}

	judgePrompt := buildJudgePrompt(input, levels, rubrics)
	return GrpoPromptOutput{
		JudgePrompt:  judgePrompt,
		LevelRubrics: rubrics,
		FrameworkRef: frameworkReference(input),
	}, nil
}

func generateLevelRubrics(ctx context.Context, provider ProviderConfig, input GrpoPromptInput, levels []string) ([]model.GrpoLevelRubric, error) {
	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": grpoRubricSystemPrompt},
			{"role": "user", "content": buildRubricUserPrompt(input, levels)},
		},
	}
	applyReasoningEffort(payload, provider)

	decoded, err := requestChatCompletion(ctx, provider, payload, 300*time.Second)
	if err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("provider returned no choices")
	}

	var generated grpoRubricPayload
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &generated); err != nil {
		return nil, err
	}

	return mapRubricsToLevels(generated, levels)
}

// mapRubricsToLevels 把模型返回的判据对齐到用户给定的档次上。
// 缺少任何一档都必须报错，不得用占位文本补齐——那会让教师模型拿到无判据的档次。
func mapRubricsToLevels(generated grpoRubricPayload, levels []string) ([]model.GrpoLevelRubric, error) {
	byLevel := make(map[string]model.GrpoLevelRubric, len(generated.LevelRubrics))
	for _, item := range generated.LevelRubrics {
		key := strings.TrimSpace(item.Level)
		if key == "" {
			continue
		}
		if _, exists := byLevel[key]; exists {
			continue
		}
		byLevel[key] = model.GrpoLevelRubric{
			Level:      key,
			Label:      strings.TrimSpace(item.Label),
			Criteria:   strings.TrimSpace(item.Criteria),
			AcceptCase: strings.TrimSpace(item.AcceptCase),
			RejectCase: strings.TrimSpace(item.RejectCase),
		}
	}

	result := make([]model.GrpoLevelRubric, 0, len(levels))
	missing := make([]string, 0)
	for _, level := range levels {
		item, ok := byLevel[level]
		if !ok || item.Criteria == "" {
			missing = append(missing, level)
			continue
		}
		if item.Label == "" {
			item.Label = "档次 " + level
		}
		result = append(result, item)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("provider did not return usable criteria for reward levels: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

// buildJudgePrompt 确定性组装最终交付给教师模型的评判提示词。
// 六个必需段落全部在这里固定，模型只贡献第三节的判据内容。
func buildJudgePrompt(input GrpoPromptInput, levels []string, rubrics []model.GrpoLevelRubric) string {
	builder := strings.Builder{}

	builder.WriteString("你是资深的长链思考数据评审专家，负责按给定档次对候选回答进行严格评判。\n\n")

	builder.WriteString("## 一、评审对象\n")
	fmt.Fprintf(&builder, "主题：%s\n", fallbackText(input.RootKeyword, "（未提供）"))
	fmt.Fprintf(&builder, "方向：%s\n", fallbackText(input.DirectionName, "（未提供）"))
	fmt.Fprintf(&builder, "问题：%s\n\n", strings.TrimSpace(input.Question))

	builder.WriteString("## 二、整体性思考框架（必须逐步核对）\n")
	if len(input.ChainSteps) == 0 {
		builder.WriteString("本方向尚未提供长链思维标准步骤。你必须在 rationale 中明确指出该回答缺少可对照的思考框架，并据此从严评判其推理完整性。\n\n")
	} else {
		builder.WriteString("本方向的长链思维标准步骤如下。评判时必须逐步核对候选回答是否覆盖并正确执行了每一步：\n")
		for index, step := range input.ChainSteps {
			fmt.Fprintf(&builder, "%d. 【%s】%s\n", index+1, fallbackText(step.Title, "未命名步骤"), strings.TrimSpace(step.Description))
			if checkpoint := strings.TrimSpace(step.Checkpoint); checkpoint != "" {
				fmt.Fprintf(&builder, "   检查点：%s\n", checkpoint)
			}
		}
		builder.WriteString("\n")
	}

	builder.WriteString("## 三、打分档次与判据\n")
	for _, rubric := range rubrics {
		fmt.Fprintf(&builder, "档次 `%s`（%s）：\n", rubric.Level, rubric.Label)
		fmt.Fprintf(&builder, "  判据：%s\n", rubric.Criteria)
		fmt.Fprintf(&builder, "  应当给分的典型情形：%s\n", fallbackText(rubric.AcceptCase, "（未提供，按判据自行判断）"))
		fmt.Fprintf(&builder, "  不应给分的典型情形：%s\n", fallbackText(rubric.RejectCase, "（未提供，按判据自行判断）"))
	}
	builder.WriteString("\n")

	builder.WriteString("## 四、结合具体场景的判断要求\n")
	builder.WriteString("- 必须结合问题中给出的具体场景要素（位置、单位、约束条件、突发情况）作出判断，不得只给抽象评价。\n")
	builder.WriteString("- 必须指出候选回答在上述思考框架的哪一步骤上偏离或缺失，并引用该步骤序号。\n")
	builder.WriteString("- 必须说明该偏离为什么落在你所选的档次，而不是相邻档次。\n\n")

	builder.WriteString("## 五、输出格式（强制）\n")
	builder.WriteString("只输出一个 JSON 对象。不要输出任何解释文字，不要使用 Markdown 代码块，不要添加额外字段：\n")
	fmt.Fprintf(&builder, "{\"level\":\"<%s>\",\"rationale\":\"<不少于 50 字的评判理由，须引用具体场景要素与对应步骤序号>\"}\n",
		strings.Join(levels, "|"))

	return builder.String()
}

func frameworkReference(input GrpoPromptInput) string {
	direction := fallbackText(input.DirectionName, "未命名方向")
	if len(input.ChainSteps) == 0 {
		return direction + " · 无标准步骤"
	}
	return fmt.Sprintf("%s · %d 步标准步骤", direction, len(input.ChainSteps))
}

func fallbackText(value, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

const grpoRubricSystemPrompt = "你是长链思考数据集的评判标准设计专家。" +
	"你只输出 JSON，不输出任何解释文字、不使用 Markdown 代码块。"

func buildRubricUserPrompt(input GrpoPromptInput, levels []string) string {
	builder := strings.Builder{}
	builder.WriteString("请为下面这个长链思考问题，设计教师模型打分所需的档次判据。\n\n")
	fmt.Fprintf(&builder, "主题：%s\n", fallbackText(input.RootKeyword, "（未提供）"))
	fmt.Fprintf(&builder, "方向：%s\n", fallbackText(input.DirectionName, "（未提供）"))
	fmt.Fprintf(&builder, "问题：%s\n\n", strings.TrimSpace(input.Question))

	if len(input.ChainSteps) > 0 {
		builder.WriteString("该方向的长链思维标准步骤（判据必须针对这些步骤设计）：\n")
		for index, step := range input.ChainSteps {
			fmt.Fprintf(&builder, "%d. 【%s】%s\n", index+1, fallbackText(step.Title, "未命名步骤"), strings.TrimSpace(step.Description))
			if checkpoint := strings.TrimSpace(step.Checkpoint); checkpoint != "" {
				fmt.Fprintf(&builder, "   检查点：%s\n", checkpoint)
			}
		}
		builder.WriteString("\n")
	}

	fmt.Fprintf(&builder, "用户给定的打分档次为：%s\n\n", strings.Join(levels, "、"))
	builder.WriteString("要求：\n")
	builder.WriteString("- criteria 必须说明该档次在「思考框架覆盖度」「场景贴合度」「推理正确性」三方面的具体表现\n")
	builder.WriteString("- acceptCase 给出一个应当判为该档的具体情形，必须结合上面的问题场景\n")
	builder.WriteString("- rejectCase 给出一个不应判为该档的具体情形\n")
	builder.WriteString("- label 是该档次的简短中文名称，不超过 8 个字\n")
	builder.WriteString("- sceneSummary 用一句话概括该问题的具体场景，不超过 60 字\n\n")
	builder.WriteString("只输出 JSON：\n")
	builder.WriteString("{\"sceneSummary\":\"...\",\"levelRubrics\":[{\"level\":\"-1\",\"label\":\"...\",\"criteria\":\"...\",\"acceptCase\":\"...\",\"rejectCase\":\"...\"}]}\n")
	fmt.Fprintf(&builder, "levelRubrics 必须恰好包含这 %d 个档次：%s，顺序一致，不得增删。\n",
		len(levels), strings.Join(levels, "、"))

	return builder.String()
}
