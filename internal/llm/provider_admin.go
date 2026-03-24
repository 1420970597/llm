package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

type providerModelsEnvelope struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Models []struct {
		ID string `json:"id"`
	} `json:"models"`
}

func FetchProviderModels(ctx context.Context, provider model.ModelProvider) ([]model.ProviderModelInfo, int, error) {
	if provider.ProviderType == "mock" {
		ids := []string{"mock-gpt-5.4", "mock-gpt", provider.Model}
		seen := map[string]struct{}{}
		models := make([]model.ProviderModelInfo, 0, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			models = append(models, model.ProviderModelInfo{ID: id})
		}
		return models, http.StatusOK, nil
	}

	if strings.TrimSpace(provider.BaseURL) == "" {
		return nil, 0, fmt.Errorf("base URL is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(provider.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(provider.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	}

	client := &http.Client{Timeout: timeoutForProvider(provider)}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return nil, res.StatusCode, fmt.Errorf("provider request failed: %s %s", res.Status, strings.TrimSpace(string(body)))
	}

	var envelope providerModelsEnvelope
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		return nil, res.StatusCode, err
	}

	seen := map[string]struct{}{}
	models := make([]model.ProviderModelInfo, 0, len(envelope.Data)+len(envelope.Models))
	for _, item := range envelope.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, model.ProviderModelInfo{ID: id})
	}
	for _, item := range envelope.Models {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, model.ProviderModelInfo{ID: id})
	}

	slices.SortFunc(models, func(a, b model.ProviderModelInfo) int {
		return strings.Compare(a.ID, b.ID)
	})
	return models, res.StatusCode, nil
}

func TestProviderConnectivity(ctx context.Context, provider model.ModelProvider) (model.ProviderConnectivityResult, error) {
	startedAt := time.Now()
	models, statusCode, err := FetchProviderModels(ctx, provider)
	latency := time.Since(startedAt).Milliseconds()
	if err != nil {
		return model.ProviderConnectivityResult{
			OK:         false,
			StatusCode: statusCode,
			LatencyMs:  latency,
			Message:    err.Error(),
		}, nil
	}

	available := make([]string, 0, len(models))
	for _, item := range models {
		available = append(available, item.ID)
	}

	configuredModel := strings.TrimSpace(provider.Model)
	modelFound := configuredModel == ""
	if configuredModel != "" {
		modelFound = slices.Contains(available, configuredModel)
	}

	message := "连通成功"
	if configuredModel != "" && !modelFound {
		message = "连通成功，但当前配置模型未出现在模型列表中"
	}

	return model.ProviderConnectivityResult{
		OK:              true,
		StatusCode:      statusCode,
		LatencyMs:       latency,
		Message:         message,
		ModelFound:      modelFound,
		AvailableModels: available,
	}, nil
}

func timeoutForProvider(provider model.ModelProvider) time.Duration {
	if provider.TimeoutSeconds > 0 {
		return time.Duration(provider.TimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}
