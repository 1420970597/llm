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

func GenerateReasoning(ctx context.Context, provider ProviderConfig, dataset model.Dataset, questions []model.Question, promptTemplate *model.PromptTemplate) ([]model.ReasoningRecord, map[int64]reasoningPayload, error) {
	records := make([]model.ReasoningRecord, 0, len(questions))
	payloads := map[int64]reasoningPayload{}
	for _, question := range questions {
		log.Printf("reasoning.generate.question.start dataset_id=%d question_id=%d", dataset.ID, question.ID)
		generated, err := generateReasoningForQuestion(ctx, provider, dataset, question, promptTemplate)
		status := "generated"
		if err != nil {
			log.Printf("reasoning.generate.question.error dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
			generated = reasoningPayload{
				Answer:    fmt.Sprintf("生成失败（question_id=%d）: %v", question.ID, err),
				Reasoning: "",
			}
			status = "failed"
		}
		payloads[question.ID] = generated
		records = append(records, model.ReasoningRecord{
			DatasetID:     dataset.ID,
			QuestionID:    question.ID,
			QuestionText:  question.Content,
			AnswerSummary: generated.Answer,
			Reasoning:     generated.Reasoning,
			Status:        status,
		})
		log.Printf("reasoning.generate.question.done dataset_id=%d question_id=%d", dataset.ID, question.ID)
	}
	return records, payloads, nil
}

func generateReasoningForQuestion(ctx context.Context, provider ProviderConfig, dataset model.Dataset, question model.Question, promptTemplate *model.PromptTemplate) (reasoningPayload, error) {
	if provider.ProviderType == "mock" {
		return reasoningPayload{}, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return reasoningPayload{}, fmt.Errorf("real provider configuration is incomplete")
	}

	systemPrompt := "You generate long-form reasoning data. Return JSON with answer and reasoning fields only."
	userPrompt := fmt.Sprintf("Question: %s", question.Content)
	if promptTemplate != nil {
		if strings.TrimSpace(promptTemplate.SystemPrompt) != "" {
			systemPrompt = promptTemplate.SystemPrompt
		}
		if strings.TrimSpace(promptTemplate.UserPrompt) != "" {
			userPrompt = strings.ReplaceAll(promptTemplate.UserPrompt, "{{question}}", question.Content)
			userPrompt = strings.ReplaceAll(userPrompt, "{{rootKeyword}}", dataset.RootKeyword)
		}
	}
	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	applyReasoningEffort(payload, provider)
	decoded, err := requestChatCompletion(ctx, provider, payload, 90*time.Second)
	if err != nil {
		return reasoningPayload{}, err
	}

	if len(decoded.Choices) == 0 {
		return reasoningPayload{}, fmt.Errorf("provider returned no choices")
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
