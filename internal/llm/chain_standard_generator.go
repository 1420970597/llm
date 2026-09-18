package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// chainStandardPayload 是模型返回的步骤载荷外层对象。
type chainStandardPayload struct {
	Steps []model.ChainStep `json:"steps"`
}

// ChainStandardInput 描述一次长链思维标准步骤生成请求。
type ChainStandardInput struct {
	RootKeyword   string
	DirectionName string
	StepCount     int
}

// defaultChainStepCount 是未指定步骤数时的默认值。
const defaultChainStepCount = 6

const chainStandardSystemPrompt = "你是长链思维训练数据专家。你为一个具体方向设计可复用的标准思考流程，用于后续生成与评估训练数据。只返回 JSON，不要解释。"

// GenerateChainStandard 为一个方向生成长链思维标准步骤。
// 返回的步骤已归一化：index 从 1 连续递增，title 非空。
func GenerateChainStandard(ctx context.Context, provider ProviderConfig, input ChainStandardInput) ([]model.ChainStep, error) {
	if provider.ProviderType == "mock" {
		return nil, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return nil, fmt.Errorf("real provider configuration is incomplete")
	}
	direction := strings.TrimSpace(input.DirectionName)
	if direction == "" {
		return nil, fmt.Errorf("direction name is required")
	}
	stepCount := input.StepCount
	if stepCount <= 0 {
		stepCount = defaultChainStepCount
	}

	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": chainStandardSystemPrompt},
			{"role": "user", "content": buildChainStandardPrompt(input.RootKeyword, direction, stepCount)},
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

	steps, err := parseChainSteps(decoded.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}
	return normalizeChainSteps(steps, stepCount)
}

// buildChainStandardPrompt 构造长链思维标准步骤的生成提示词。
func buildChainStandardPrompt(rootKeyword, direction string, stepCount int) string {
	builder := strings.Builder{}
	if strings.TrimSpace(rootKeyword) != "" {
		builder.WriteString(fmt.Sprintf("主题：%s\n", strings.TrimSpace(rootKeyword)))
	}
	builder.WriteString(fmt.Sprintf("方向：%s\n\n", direction))
	builder.WriteString(fmt.Sprintf("请为该方向设计 %d 个长链思维标准步骤，形成一套可复用的思考流程。\n", stepCount))
	builder.WriteString("要求：\n")
	builder.WriteString("1. 每个步骤都要有实质推理动作，不能是「分析情况」这类空话。\n")
	builder.WriteString("2. 步骤之间必须有依赖关系：后一步要消费前一步的结论。\n")
	builder.WriteString("3. description 写清这一步具体做什么、依据什么、产出什么中间结论。\n")
	builder.WriteString("4. checkpoint 写清这一步完成的可验证判据（如何确认这一步做对了）。\n")
	builder.WriteString("5. 步骤要覆盖：目标澄清、约束识别、方案推演、风险校验、结论收敛。\n\n")
	builder.WriteString("只返回如下 JSON，不要输出其他内容：\n")
	builder.WriteString(`{"steps":[{"index":1,"title":"步骤标题","description":"这一步做什么","checkpoint":"完成判据"}]}`)
	return builder.String()
}

// parseChainSteps 解析模型返回的步骤。兼容外层对象与裸数组两种形态。
func parseChainSteps(content string) ([]model.ChainStep, error) {
	var payload chainStandardPayload
	if err := unmarshalStructuredContent(content, &payload); err == nil && len(payload.Steps) > 0 {
		return payload.Steps, nil
	}

	// 部分模型会直接返回数组，此时外层对象解析会失败，退回数组解析。
	var bare []model.ChainStep
	if err := unmarshalStructuredContent(content, &bare); err == nil && len(bare) > 0 {
		return bare, nil
	}

	// 再退一步：数组元素可能是纯字符串。
	var titles []string
	if err := unmarshalStructuredContent(content, &titles); err == nil && len(titles) > 0 {
		steps := make([]model.ChainStep, 0, len(titles))
		for _, title := range titles {
			steps = append(steps, model.ChainStep{Title: title})
		}
		return steps, nil
	}

	return nil, fmt.Errorf("provider returned no usable chain steps")
}

// normalizeChainSteps 归一化步骤：丢弃空标题，重排 index 为 1..N，并裁剪到 limit。
func normalizeChainSteps(steps []model.ChainStep, limit int) ([]model.ChainStep, error) {
	normalized := make([]model.ChainStep, 0, len(steps))
	for _, step := range steps {
		title := strings.TrimSpace(step.Title)
		if title == "" {
			continue
		}
		normalized = append(normalized, model.ChainStep{
			Index:       len(normalized) + 1,
			Title:       title,
			Description: strings.TrimSpace(step.Description),
			Checkpoint:  strings.TrimSpace(step.Checkpoint),
		})
		if limit > 0 && len(normalized) >= limit {
			break
		}
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("provider returned no usable chain steps")
	}
	return normalized, nil
}

// MarshalChainSteps 把步骤序列编码为 JSONB 可存的形式。
func MarshalChainSteps(steps []model.ChainStep) ([]byte, error) {
	if steps == nil {
		steps = []model.ChainStep{}
	}
	return json.Marshal(steps)
}
