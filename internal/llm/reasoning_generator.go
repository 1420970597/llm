package llm

import (
	"context"
	"fmt"
	"log"
	"strings"
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
		log.Printf("reasoning.generate.question.start dataset_id=%d question_id=%d", dataset.ID, question.ID)
		generated, err := generateReasoningForQuestion(ctx, provider, dataset, question)
		if err != nil {
			log.Printf("reasoning.generate.question.error dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
			generated = reasoningPayload{
				Answer:    fmt.Sprintf("生成失败（question_id=%d）: %v", question.ID, err),
				Reasoning: "",
			}
		}
		payloads[question.ID] = generated
		records = append(records, model.ReasoningRecord{
			DatasetID:     dataset.ID,
			QuestionID:    question.ID,
			QuestionText:  question.Content,
			AnswerSummary: generated.Answer,
			Status:        "generated",
		})
		log.Printf("reasoning.generate.question.done dataset_id=%d question_id=%d", dataset.ID, question.ID)
	}
	return records, payloads, nil
}

func generateReasoningForQuestion(ctx context.Context, provider ProviderConfig, dataset model.Dataset, question model.Question) (reasoningPayload, error) {
	if provider.ProviderType == "mock" {
		return reasoningPayload{}, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return reasoningPayload{}, fmt.Errorf("real provider configuration is incomplete")
	}

	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You generate long-form reasoning data. Return JSON with answer and reasoning fields only."},
			{"role": "user", "content": fmt.Sprintf("Question: %s", question.Content)},
		},
	}
	applyReasoningEffort(payload, provider)
	decoded, err := requestChatCompletion(ctx, provider, payload, 90*time.Second)
	if err != nil {
		return reasoningPayload{}, err
	}

	var generated reasoningPayload
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &generated); err != nil {
		fallback := strings.TrimSpace(decoded.Choices[0].Message.Content)
		if fallback == "" {
			return reasoningPayload{}, err
		}
		return reasoningPayload{
			Answer:    fallback,
			Reasoning: "",
		}, nil
	}
	return generated, nil
}
