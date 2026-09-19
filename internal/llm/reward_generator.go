package llm

import (
	"context"
	"errors"
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
			if errors.Is(err, ErrInvalidContent) {
				// 同 reasoning：模型返回了可解析但占位的评分理由，
				// 标记 invalid 以便与网络失败区分计数。
				status = ContentStatusInvalid
				log.Printf("reward.generate.question.invalid dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
			} else {
				log.Printf("reward.generate.question.error dataset_id=%d question_id=%d err=%v", dataset.ID, question.ID, err)
				generated = rewardPayload{
					Score:     0,
					Rationale: fmt.Sprintf("生成失败（question_id=%d）: %v", question.ID, err),
				}
				status = "failed"
			}
		}
		payloads[question.ID] = generated
		records = append(records, model.RewardRecord{
			DatasetID:    dataset.ID,
			QuestionID:   question.ID,
			QuestionText: question.Content,
			Score:        generated.Score,
			Status:       status,
		})
		log.Printf("reward.generate.question.done dataset_id=%d question_id=%d status=%s", dataset.ID, question.ID, status)
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

	if len(decoded.Choices) == 0 {
		return rewardPayload{}, fmt.Errorf("provider returned no choices")
	}
	var generated rewardPayload
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &generated); err != nil {
		// 同 reasoning_generator：解析失败如实返回错误（即 failed），
		// 不再把非 JSON 文本当作有效评分返回 nil 错误。保留原始文本供核查。
		fallback := strings.TrimSpace(decoded.Choices[0].Message.Content)
		if fallback != "" {
			return rewardPayload{Score: 0, Rationale: fallback}, err
		}
		return rewardPayload{}, err
	}

	// 结构层通过不代表内容可用：rationale 是评分的唯一依据，
	// 占位理由意味着这条评分没有实际判断过程。
	if assessment := AssessRewardContent(generated.Rationale); !assessment.Valid {
		return generated, newInvalidContentError(assessment)
	}
	return generated, nil
}
