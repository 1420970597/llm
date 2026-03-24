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
	"github.com/1420970597/llm/internal/store"
)

type questionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func GenerateQuestions(ctx context.Context, provider ProviderConfig, dataset model.Dataset, domains []model.Domain) ([]model.Question, error) {
	questions := make([]model.Question, 0, len(domains)*max(dataset.Estimate.QuestionsPerDomain, 1))
	for _, domain := range domains {
		generated, err := generateQuestionsForDomain(ctx, provider, dataset, domain)
		if err != nil {
			return nil, err
		}
		questions = append(questions, generated...)
	}
	return questions, nil
}

func generateQuestionsForDomain(ctx context.Context, provider ProviderConfig, dataset model.Dataset, domain model.Domain) ([]model.Question, error) {
	count := max(dataset.Estimate.QuestionsPerDomain, 1)
	if provider.ProviderType == "mock" || provider.APIKey == "" || provider.BaseURL == "" {
		return mockQuestions(dataset, domain, count), nil
	}

	prompt := fmt.Sprintf("Generate %d unique user questions for the domain '%s' under root keyword '%s'. Return a JSON array of strings only.", count, domain.Name, dataset.RootKeyword)
	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You generate diverse, non-duplicated training questions. Return JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.7,
	}
	applyReasoningEffort(payload, provider)
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("provider request failed: %s", res.Status)
	}

	var decoded questionResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("provider returned no choices")
	}

	var texts []string
	if err := json.Unmarshal([]byte(decoded.Choices[0].Message.Content), &texts); err != nil {
		return nil, err
	}

	questions := make([]model.Question, 0, len(texts))
	for _, text := range texts {
		questions = append(questions, model.Question{
			DatasetID:     dataset.ID,
			DomainID:      domain.ID,
			DomainName:    domain.Name,
			Content:       strings.TrimSpace(text),
			CanonicalHash: store.CanonicalHash(text),
			Status:        "generated",
		})
	}
	return questions, nil
}

func mockQuestions(dataset model.Dataset, domain model.Domain, count int) []model.Question {
	questions := make([]model.Question, 0, count)
	for index := 0; index < count; index++ {
		text := fmt.Sprintf("在 %s 场景 %02d 中，如何围绕 %s 制定高质量推理任务？", domain.Name, index+1, dataset.RootKeyword)
		questions = append(questions, model.Question{
			DatasetID:     dataset.ID,
			DomainID:      domain.ID,
			DomainName:    domain.Name,
			Content:       text,
			CanonicalHash: store.CanonicalHash(text),
			Status:        "generated",
		})
	}
	return questions
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
