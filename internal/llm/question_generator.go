package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

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
	decoded, err := requestChatCompletion(ctx, provider, payload, 60*time.Second)
	if err != nil {
		return nil, err
	}

	var texts []string
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &texts); err != nil {
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
