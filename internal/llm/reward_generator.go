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

func GenerateRewards(ctx context.Context, provider ProviderConfig, dataset model.Dataset, questions []model.Question) ([]model.RewardRecord, map[int64]rewardPayload, error) {
	records := make([]model.RewardRecord, 0, len(questions))
	payloads := map[int64]rewardPayload{}
	for _, question := range questions {
		log.Printf("reward.generate.question.start dataset_id=%d question_id=%d", dataset.ID, question.ID)
		generated, err := generateRewardForQuestion(ctx, provider, dataset, question)
		if err != nil {
			log.Printf("reward.generate.question.error dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
			generated = rewardPayload{
				Score:     0,
				Rationale: fmt.Sprintf("生成失败（question_id=%d）: %v", question.ID, err),
			}
		}
		payloads[question.ID] = generated
		records = append(records, model.RewardRecord{
			DatasetID:    dataset.ID,
			QuestionID:   question.ID,
			QuestionText: question.Content,
			Score:        generated.Score,
			Status:       "generated",
		})
		log.Printf("reward.generate.question.done dataset_id=%d question_id=%d", dataset.ID, question.ID)
	}
	return records, payloads, nil
}

func generateRewardForQuestion(ctx context.Context, provider ProviderConfig, dataset model.Dataset, question model.Question) (rewardPayload, error) {
	if provider.ProviderType == "mock" || provider.APIKey == "" || provider.BaseURL == "" {
		return rewardPayload{
			Score:     0.85,
			Rationale: fmt.Sprintf("围绕 %s 的问题具有清晰目标、可评估约束和可展开的推理步骤，适合作为奖励评估样本。", dataset.RootKeyword),
		}, nil
	}

	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You generate reward-model data. Return JSON with score and rationale only."},
			{"role": "user", "content": fmt.Sprintf("Question: %s", question.Content)},
		},
	}
	applyReasoningEffort(payload, provider)
	decoded, err := requestChatCompletion(ctx, provider, payload, 20*time.Second)
	if err != nil {
		return rewardPayload{}, err
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
