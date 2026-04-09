package llm

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

type rewardPayload struct {
	Score     float64 `json:"score"`
	Rationale string  `json:"rationale"`
}

func GenerateRewards(ctx context.Context, provider ProviderConfig, dataset model.Dataset, questions []model.Question, promptTemplate *model.PromptTemplate) ([]model.RewardRecord, map[int64]rewardPayload, error) {
	records := make([]model.RewardRecord, 0, len(questions))
	payloads := map[int64]rewardPayload{}
	for _, question := range questions {
		log.Printf("reward.generate.question.start dataset_id=%d question_id=%d", dataset.ID, question.ID)
		generated, err := generateRewardForQuestion(ctx, provider, dataset, question, promptTemplate)
		status := "generated"
		if err != nil {
			log.Printf("reward.generate.question.error dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
			generated = rewardPayload{
				Score:     0,
				Rationale: fmt.Sprintf("生成失败（question_id=%d）: %v", question.ID, err),
			}
			status = "failed"
		}
		payloads[question.ID] = generated
		records = append(records, model.RewardRecord{
			DatasetID:    dataset.ID,
			QuestionID:   question.ID,
			QuestionText: question.Content,
			Score:        generated.Score,
			Status:       status,
		})
		log.Printf("reward.generate.question.done dataset_id=%d question_id=%d", dataset.ID, question.ID)
	}
	return records, payloads, nil
}

func generateRewardForQuestion(ctx context.Context, provider ProviderConfig, dataset model.Dataset, question model.Question, promptTemplate *model.PromptTemplate) (rewardPayload, error) {
	if provider.ProviderType == "mock" {
		return rewardPayload{}, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return rewardPayload{}, fmt.Errorf("real provider configuration is incomplete")
	}

	systemPrompt := "You generate reward-model data. Return JSON with score and rationale only."
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
	decoded, err := requestChatCompletion(ctx, provider, payload, 60*time.Second)
	if err != nil {
		return rewardPayload{}, err
	}

	if len(decoded.Choices) == 0 {
		return rewardPayload{}, fmt.Errorf("provider returned no choices")
	}
	var generated rewardPayload
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &generated); err != nil {
		fallback := strings.TrimSpace(decoded.Choices[0].Message.Content)
		if fallback == "" {
			return rewardPayload{}, err
		}
		return rewardPayload{
			Score:     0,
			Rationale: fallback,
		}, nil
	}
	return generated, nil
}
