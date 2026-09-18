package llm

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// SftInput 生成一条 SFT 样本所需的全部输入。
// Steps 是该问题所属「方向」的长链思维标准步骤（由 L2 生成，可为空）。
type SftInput struct {
	DatasetID      int64
	RootKeyword    string
	Question       model.Question
	Steps          []model.ChainStep
	IncludeAnswer  bool
	PromptTemplate *model.PromptTemplate
}

// SftPayload 模型返回的 SFT 样本主体。
type SftPayload struct {
	ChainOfThought string `json:"chainOfThought"`
	Answer         string `json:"answer"`
}

const sftSystemPrompt = "你是长链思考训练数据的构造者。必须严格按给定步骤逐步推理，每一步都给出中间结论，只返回 JSON 对象。"

// sftTimeout 该模型为推理型，单次响应可达 120 秒以上，留足余量。
const sftTimeout = 300 * time.Second

// GenerateSft 为单个问题生成思维链，以及可选的最终答案。
func GenerateSft(ctx context.Context, provider ProviderConfig, input SftInput) (SftPayload, error) {
	if provider.ProviderType == "mock" {
		return SftPayload{}, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return SftPayload{}, fmt.Errorf("real provider configuration is incomplete")
	}

	systemPrompt, userPrompt := buildSftPrompt(input)
	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	applyReasoningEffort(payload, provider)

	decoded, err := requestChatCompletion(ctx, provider, payload, sftTimeout)
	if err != nil {
		return SftPayload{}, err
	}
	if len(decoded.Choices) == 0 {
		return SftPayload{}, fmt.Errorf("provider returned no choices")
	}

	result, err := parseSftPayload(decoded.Choices[0].Message.Content)
	if err != nil {
		return SftPayload{}, err
	}
	if !input.IncludeAnswer {
		result.Answer = ""
	}
	if result.ChainOfThought == "" {
		return SftPayload{}, fmt.Errorf("provider returned empty chain of thought for question %d", input.Question.ID)
	}

	steps := NormalizeChainSteps(input.Steps)
	log.Printf("sft.generate.question.done dataset_id=%d question_id=%d cot_runes=%d aligned_steps=%d/%d",
		input.DatasetID, input.Question.ID, len([]rune(result.ChainOfThought)),
		CountAlignedSteps(result.ChainOfThought, steps), len(steps))
	return result, nil
}

// buildSftPrompt 组装系统提示词与用户提示词。独立出来便于单测覆盖提示词内容。
func buildSftPrompt(input SftInput) (systemPrompt string, userPrompt string) {
	steps := NormalizeChainSteps(input.Steps)
	systemPrompt = sftSystemPrompt
	userPrompt = defaultSftUserPrompt(input, steps)

	if input.PromptTemplate != nil {
		if strings.TrimSpace(input.PromptTemplate.SystemPrompt) != "" {
			systemPrompt = input.PromptTemplate.SystemPrompt
		}
		if strings.TrimSpace(input.PromptTemplate.UserPrompt) != "" {
			userPrompt = input.PromptTemplate.UserPrompt
			userPrompt = strings.ReplaceAll(userPrompt, "{{question}}", input.Question.Content)
			userPrompt = strings.ReplaceAll(userPrompt, "{{rootKeyword}}", input.RootKeyword)
			userPrompt = strings.ReplaceAll(userPrompt, "{{steps}}", formatChainSteps(steps))
		}
	}
	return systemPrompt, userPrompt
}

func defaultSftUserPrompt(input SftInput, steps []model.ChainStep) string {
	answerRule := "4. answer 字段给出该问题的最终结论（具体、可执行，不要复述题目）。"
	if !input.IncludeAnswer {
		answerRule = "4. answer 字段返回空字符串，本次只需要思维链。"
	}

	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("主题：%s\n", input.RootKeyword))
	builder.WriteString(fmt.Sprintf("问题：%s\n\n", input.Question.Content))
	if len(steps) == 0 {
		builder.WriteString("该方向尚未生成长链思维标准步骤，请自行拆解出 5~8 个推理步骤。\n\n")
	} else {
		builder.WriteString("该方向的长链思维标准步骤（必须逐步对应）：\n")
		builder.WriteString(formatChainSteps(steps))
		builder.WriteString("\n")
	}
	builder.WriteString("要求：\n")
	builder.WriteString("1. chainOfThought 必须逐步对应上述每一步，每一步都写出该步的中间结论，而不是只罗列步骤名称。\n")
	builder.WriteString("2. 每一步结束时明确回答该步的检查点（checkpoint）是否满足，以及依据。\n")
	builder.WriteString("3. chainOfThought 必须是完整可读的长链推理过程，禁止使用「首先、其次、最后」这类无信息量的套话。\n")
	builder.WriteString(answerRule)
	builder.WriteString("\n5. 只返回 JSON 对象：{\"chainOfThought\":\"...\",\"answer\":\"...\"}，不要输出解释文字，不要使用 Markdown 代码块。")
	return builder.String()
}

func formatChainSteps(steps []model.ChainStep) string {
	if len(steps) == 0 {
		return "（无）\n"
	}
	builder := strings.Builder{}
	for _, step := range steps {
		builder.WriteString(fmt.Sprintf("- 第%d步 %s", step.Index, step.Title))
		if step.Description != "" {
			builder.WriteString("：")
			builder.WriteString(step.Description)
		}
		if step.Checkpoint != "" {
			builder.WriteString(fmt.Sprintf("（检查点：%s）", step.Checkpoint))
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// parseSftPayload 解析模型返回的 SFT 样本。
// 模型偶尔会只返回思维链正文而不带 JSON 包装，此时保留正文而不是丢弃整条样本。
func parseSftPayload(raw string) (SftPayload, error) {
	var payload SftPayload
	if err := unmarshalStructuredContent(raw, &payload); err != nil {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return SftPayload{}, fmt.Errorf("provider returned empty content")
		}
		log.Printf("sft.generate.parse_fallback err=%v", err)
		return SftPayload{ChainOfThought: trimmed}, nil
	}
	payload.ChainOfThought = strings.TrimSpace(payload.ChainOfThought)
	payload.Answer = strings.TrimSpace(payload.Answer)
	return payload, nil
}

// NormalizeChainSteps 去掉空步骤并按 1 起重新编号，保证步骤序号连续可用于对齐检查。
func NormalizeChainSteps(steps []model.ChainStep) []model.ChainStep {
	normalized := make([]model.ChainStep, 0, len(steps))
	for _, step := range steps {
		title := strings.TrimSpace(step.Title)
		description := strings.TrimSpace(step.Description)
		checkpoint := strings.TrimSpace(step.Checkpoint)
		if title == "" && description == "" && checkpoint == "" {
			continue
		}
		normalized = append(normalized, model.ChainStep{
			Index:       len(normalized) + 1,
			Title:       title,
			Description: description,
			Checkpoint:  checkpoint,
		})
	}
	return normalized
}

// CountAlignedSteps 统计思维链中出现了多少个长链标准步骤。
// 命中判定：思维链包含该步骤标题，或包含「第N步」字样。
func CountAlignedSteps(chainOfThought string, steps []model.ChainStep) int {
	if chainOfThought == "" {
		return 0
	}
	aligned := 0
	for _, step := range NormalizeChainSteps(steps) {
		if step.Title != "" && strings.Contains(chainOfThought, step.Title) {
			aligned++
			continue
		}
		if strings.Contains(chainOfThought, fmt.Sprintf("第%d步", step.Index)) {
			aligned++
		}
	}
	return aligned
}

// SftStatus 依据对齐情况判定样本状态。
// partial 表示思维链没有对上任何一个标准步骤，样本可用但对齐质量不足。
func SftStatus(alignedSteps, totalSteps int, generationErr error) string {
	if generationErr != nil {
		return "failed"
	}
	if totalSteps > 0 && alignedSteps == 0 {
		return "partial"
	}
	return "generated"
}
