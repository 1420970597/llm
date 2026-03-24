package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

type rewardPayload struct {
	Score     float64 `json:"score"`
	Rationale string  `json:"rationale"`
}

type rewardResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func GenerateRewards(ctx context.Context, provider ProviderConfig, dataset model.Dataset, questions []model.Question) ([]model.RewardRecord, map[int64]rewardPayload, error) {
	records := make([]model.RewardRecord, 0, len(questions))
	payloads := map[int64]rewardPayload{}
	for _, question := range questions {
		generated, err := generateRewardForQuestion(ctx, provider, dataset, question)
		if err != nil {
			return nil, nil, err
		}
		payloads[question.ID] = generated
		records = append(records, model.RewardRecord{
			DatasetID:    dataset.ID,
			QuestionID:   question.ID,
			QuestionText: question.Content,
			Score:        generated.Score,
			Status:       "generated",
		})
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
		"temperature": 0.4,
	}
	applyReasoningEffort(payload, provider)
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return rewardPayload{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return rewardPayload{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return rewardPayload{}, fmt.Errorf("provider request failed: %s", res.Status)
	}

	var decoded rewardResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return rewardPayload{}, err
	}
	if len(decoded.Choices) == 0 {
		return rewardPayload{}, fmt.Errorf("provider returned no choices")
	}

	var generated rewardPayload
	if err := json.Unmarshal([]byte(decoded.Choices[0].Message.Content), &generated); err != nil {
		return rewardPayload{}, err
	}
	return generated, nil
}
