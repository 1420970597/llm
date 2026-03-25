package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/1420970597/llm/internal/model"
)

type reasoningPayload struct {
	Answer    string `json:"answer"`
	Reasoning string `json:"reasoning"`
}

func GenerateReasoning(ctx context.Context, provider ProviderConfig, dataset model.Dataset, questions []model.Question) ([]model.ReasoningRecord, map[int64]reasoningPayload, error) {
	records := make([]model.ReasoningRecord, 0, len(questions))
	payloads := map[int64]reasoningPayload{}
	for _, question := range questions {
		generated, err := generateReasoningForQuestion(ctx, provider, dataset, question)
		if err != nil {
			return nil, nil, err
		}
		payloads[question.ID] = generated
		records = append(records, model.ReasoningRecord{
			DatasetID:     dataset.ID,
			QuestionID:    question.ID,
			QuestionText:  question.Content,
			AnswerSummary: generated.Answer,
			Status:        "generated",
		})
	}
	return records, payloads, nil
}

func generateReasoningForQuestion(ctx context.Context, provider ProviderConfig, dataset model.Dataset, question model.Question) (reasoningPayload, error) {
	if provider.ProviderType == "mock" || provider.APIKey == "" || provider.BaseURL == "" {
		return reasoningPayload{
			Answer:    fmt.Sprintf("围绕问题“%s”的高质量答案摘要。", question.Content),
			Reasoning: fmt.Sprintf("长思维链示例：先识别 %s 的核心目标，再拆解约束、参与方、行动路径、风险与评估指标，最终组织成可训练的推理步骤。", dataset.RootKeyword),
		}, nil
	}

	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You generate long-form reasoning data. Return JSON with answer and reasoning fields only."},
			{"role": "user", "content": fmt.Sprintf("Question: %s", question.Content)},
		},
		"temperature": 0.6,
	}
	applyReasoningEffort(payload, provider)
	decoded, err := requestChatCompletion(ctx, provider, payload, 90*time.Second)
	if err != nil {
		return reasoningPayload{}, err
	}

	var generated reasoningPayload
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &generated); err != nil {
		return reasoningPayload{}, err
	}
	return generated, nil
}
