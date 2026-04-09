package llm

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

func GenerateQuestions(ctx context.Context, provider ProviderConfig, dataset model.Dataset, domains []model.Domain, promptTemplate *model.PromptTemplate) ([]model.Question, error) {
	questions := make([]model.Question, 0, len(domains)*max(dataset.Estimate.QuestionsPerDomain, 1))
	for _, domain := range domains {
		log.Printf("questions.generate.domain.start dataset_id=%d domain_id=%d domain=%q", dataset.ID, domain.ID, domain.Name)
		generated, err := generateQuestionsForDomain(ctx, provider, dataset, domain, promptTemplate)
		if err != nil {
			log.Printf("questions.generate.domain.error dataset_id=%d domain_id=%d domain=%q err=%v", dataset.ID, domain.ID, domain.Name, err)
			return nil, err
		}
		questions = append(questions, generated...)
		log.Printf("questions.generate.domain.done dataset_id=%d domain_id=%d generated=%d", dataset.ID, domain.ID, len(generated))
	}
	return questions, nil
}

func generateQuestionsForDomain(ctx context.Context, provider ProviderConfig, dataset model.Dataset, domain model.Domain, promptTemplate *model.PromptTemplate) ([]model.Question, error) {
	count := max(dataset.Estimate.QuestionsPerDomain, 1)
	if provider.ProviderType == "mock" {
		return nil, fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if provider.APIKey == "" || provider.BaseURL == "" {
		return nil, fmt.Errorf("real provider configuration is incomplete")
	}

	prompt := fmt.Sprintf("Generate %d unique user questions for the domain '%s' under root keyword '%s'. Return a JSON array of strings only.", count, domain.Name, dataset.RootKeyword)
	systemPrompt := "You generate diverse, non-duplicated training questions. Return JSON only."
	if promptTemplate != nil {
		if strings.TrimSpace(promptTemplate.SystemPrompt) != "" {
			systemPrompt = promptTemplate.SystemPrompt
		}
		if strings.TrimSpace(promptTemplate.UserPrompt) != "" {
			prompt = strings.ReplaceAll(promptTemplate.UserPrompt, "{{rootKeyword}}", dataset.RootKeyword)
			prompt = strings.ReplaceAll(prompt, "{{domainName}}", domain.Name)
			prompt = strings.ReplaceAll(prompt, "{{count}}", fmt.Sprintf("%d", count))
		}
	}
	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		},
	}
	applyReasoningEffort(payload, provider)
	decoded, err := requestChatCompletion(ctx, provider, payload, 60*time.Second)
	if err != nil {
		return nil, err
	}

	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("provider returned no choices")
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
