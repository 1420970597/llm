package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

type domainResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func GenerateDomains(ctx context.Context, provider ProviderConfig, dataset model.Dataset) ([]model.Domain, []model.DomainEdge, error) {
	if provider.ProviderType == "mock" || provider.APIKey == "" || provider.BaseURL == "" {
		return mockDomains(dataset), mockEdges(dataset), nil
	}

	count := dataset.Estimate.DomainCount
	if count <= 0 {
		count = 100
	}

	prompt := fmt.Sprintf("Generate %d unique domain names for the keyword '%s'. Return a JSON array of strings only. Avoid duplicates.", count, dataset.RootKeyword)
	payload := map[string]any{
		"model": provider.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "You generate unique domain lists for a knowledge graph. Return JSON only."},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.4,
	}
	applyReasoningEffort(payload, provider)

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("provider request failed: %s", res.Status)
	}

	var decoded domainResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return nil, nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, nil, fmt.Errorf("provider returned no choices")
	}

	raw := decoded.Choices[0].Message.Content
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, nil, err
	}

	canonicalSeen := map[string]struct{}{}
	domains := make([]model.Domain, 0, len(names))
	for _, name := range names {
		canonical := canonicalize(name)
		if canonical == "" {
			continue
		}
		if _, exists := canonicalSeen[canonical]; exists {
			continue
		}
		canonicalSeen[canonical] = struct{}{}
		domains = append(domains, model.Domain{
			Name:         strings.TrimSpace(name),
			Canonical:    canonical,
			Level:        1,
			Source:       "ai",
			ReviewStatus: "draft",
		})
	}

	sort.Slice(domains, func(i, j int) bool { return domains[i].Name < domains[j].Name })
	edges := make([]model.DomainEdge, 0, len(domains))
	return domains, edges, nil
}

func ResolveAPIKey(masked, decrypted string) string {
	if decrypted != "" {
		return decrypted
	}
	return appcrypto.MaskSecret(masked)
}

func mockDomains(dataset model.Dataset) []model.Domain {
	facets := []string{"Operations", "Systems", "Scenarios", "Tactics", "Capabilities", "Threat Models", "Logistics", "Decision Paths", "Training Contexts", "Evaluation Tracks"}
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
