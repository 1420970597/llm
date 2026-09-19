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

	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("provider returned no choices")
	}
	var texts []string
	if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &texts); err != nil {
		return nil, err
	}

	questions := make([]model.Question, 0, len(texts))
	for index, text := range texts {
		content := strings.TrimSpace(text)
		// 结构层通过不代表内容可用：模型会返回 ["...", "N/A"] 这类占位数组。
		//
		// 问题不能像推理记录那样标 invalid 落库：questions.status 由 store 层
		// 硬编码为 'generated'（pipeline_store.go / question_store_v2.go），没有
		// invalid 通道。因此对不合格的问题直接丢弃，不入库。
		if assessment := AssessQuestionContent(content); !assessment.Valid {
			log.Printf("questions.generate.content.invalid domain_id=%d reason=%s", domain.ID, assessment.Reason)
			continue
		}
		level, score := AssignDifficulty(index, len(texts))
		questions = append(questions, model.Question{
			DatasetID:         dataset.ID,
			DomainID:          domain.ID,
			DomainName:        domain.Name,
			DirectionDomainID: domain.ID,
			Content:           content,
			CanonicalHash:     store.CanonicalHash(content),
			DedupeKey:         store.CanonicalHash(content),
			Difficulty:        level,
			DifficultyScore:   score,
			Source:            "ai",
			Status:            "generated",
		})
	}
	if len(questions) == 0 {
		// 全部被内容校验拦下：不能返回空切片，否则调用方会把数据集
		// 推进到 questions_generated 却一条问题都没有。
		return nil, newInvalidContentError(LongTextAssessment{
			Field:  "content",
			Reason: fmt.Sprintf("领域 %q 的 %d 条问题全部为占位内容", domain.Name, len(texts)),
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
