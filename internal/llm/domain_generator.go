package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
)

type ProviderConfig struct {
	BaseURL         string
	Model           string
	ProviderType    string
	ReasoningEffort string
	APIKey          string
}

func GenerateDomains(ctx context.Context, provider ProviderConfig, dataset model.Dataset) ([]model.Domain, []model.DomainEdge, error) {
	if provider.ProviderType == "mock" || provider.APIKey == "" || provider.BaseURL == "" {
		return mockDomains(dataset), mockEdges(dataset), nil
	}

	count := dataset.Estimate.DomainCount
	if count <= 0 {
		count = 100
	}

	domains, err := generateDomainsInBatches(ctx, provider, dataset, count)
	if err != nil {
		return nil, nil, err
	}

	sort.Slice(domains, func(i, j int) bool { return domains[i].Name < domains[j].Name })
	edges := make([]model.DomainEdge, 0, len(domains))
	return domains, edges, nil
}

func generateDomainsInBatches(ctx context.Context, provider ProviderConfig, dataset model.Dataset, targetCount int) ([]model.Domain, error) {
	batchSize := 25
	switch {
	case targetCount > 600:
		batchSize = 50
	case targetCount > 250:
		batchSize = 40
	case targetCount < 25:
		batchSize = targetCount
	}

	roundLimit := max(3, (targetCount+batchSize-1)/batchSize*3)
	collected := make([]model.Domain, 0, targetCount)
	seen := map[string]struct{}{}

	for round := 0; round < roundLimit && len(collected) < targetCount; round++ {
		remaining := targetCount - len(collected)
		requestCount := min(batchSize, remaining)

		prompt := buildDomainPrompt(dataset.RootKeyword, requestCount, collected)
		payload := map[string]any{
			"model": provider.Model,
			"messages": []map[string]string{
				{"role": "system", "content": "You generate unique domain lists for a knowledge graph. Return JSON only."},
				{"role": "user", "content": prompt},
			},
			"temperature": 0.4,
		}

		decoded, err := requestChatCompletion(ctx, provider, payload, 90*time.Second)
		if err != nil {
			if batchSize > 10 {
				batchSize = max(10, batchSize/2)
				continue
			}
			return nil, err
		}

		var names []string
		if err := unmarshalStructuredContent(decoded.Choices[0].Message.Content, &names); err != nil {
			return nil, err
		}

		for _, name := range names {
			canonical := canonicalize(name)
			if canonical == "" {
				continue
			}
			if _, exists := seen[canonical]; exists {
				continue
			}
			seen[canonical] = struct{}{}
			collected = append(collected, model.Domain{
				Name:         strings.TrimSpace(name),
				Canonical:    canonical,
				Level:        1,
				Source:       "ai",
				ReviewStatus: "draft",
			})
			if len(collected) >= targetCount {
				break
			}
		}
	}

	if len(collected) == 0 {
		return nil, fmt.Errorf("provider returned no domains")
	}
	if len(collected) < targetCount {
		return nil, fmt.Errorf("provider only produced %d unique domains out of requested %d; please lower the domain count or retry", len(collected), targetCount)
	}
	return collected[:targetCount], nil
}

func buildDomainPrompt(rootKeyword string, count int, existing []model.Domain) string {
	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("Generate %d unique domain names for the keyword '%s'. Return a JSON array of strings only. Avoid duplicates.", count, rootKeyword))
	if len(existing) > 0 {
		builder.WriteString(" Already generated domains, do not repeat any of these: ")
		limit := min(len(existing), 120)
		for index := 0; index < limit; index++ {
			if index > 0 {
				builder.WriteString(", ")
			}
			builder.WriteString(existing[index].Name)
		}
		builder.WriteString(".")
	}
	return builder.String()
}

func ResolveAPIKey(masked, decrypted string) string {
	if decrypted != "" {
		return decrypted
	}
	return appcrypto.MaskSecret(masked)
}

func mockDomains(dataset model.Dataset) []model.Domain {
	facets := []string{"基础概念", "常见场景", "核心问题", "关键方法", "实操步骤", "风险误区", "进阶技巧", "工具资源", "评估指标", "案例复盘"}
	domains := make([]model.Domain, 0, dataset.Estimate.DomainCount)
	for i := 0; i < dataset.Estimate.DomainCount; i++ {
		facet := facets[i%len(facets)]
		name := fmt.Sprintf("%s %s %03d", dataset.RootKeyword, facet, i+1)
		domains = append(domains, model.Domain{
			Name:         name,
			Canonical:    canonicalize(name),
			Level:        1,
			Source:       "mock",
			ReviewStatus: "draft",
		})
	}
	return domains
}

func mockEdges(dataset model.Dataset) []model.DomainEdge {
	return []model.DomainEdge{}
}

func canonicalize(input string) string {
	lowered := strings.ToLower(strings.TrimSpace(input))
	lowered = strings.ReplaceAll(lowered, "_", " ")
	lowered = strings.Join(strings.Fields(lowered), " ")
	return lowered
}
