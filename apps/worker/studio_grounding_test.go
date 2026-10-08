package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

type groundingVersions map[int64]store.DocumentVersion

func (versions groundingVersions) GetVersionByID(_ context.Context, id int64) (store.DocumentVersion, error) {
	value, ok := versions[id]
	if !ok {
		return value, fmt.Errorf("missing version %d", id)
	}
	return value, nil
}

type groundingChunks []model.SourceChunk

func (chunks groundingChunks) GetChunks(_ context.Context, projectID int64, ids []int64) ([]model.SourceChunk, error) {
	result := []model.SourceChunk{}
	for _, id := range ids {
		for _, chunk := range chunks {
			if chunk.ID == id && chunk.ProjectID == projectID {
				result = append(result, chunk)
			}
		}
	}
	return result, nil
}

func TestQuestionForUsesOnlyFrozenMaterials(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		prompt := request.Messages[len(request.Messages)-1].Content
		for _, wanted := range []string{"2～8℃", "冷链温控", `id="7"`, "第 2 个独立场景"} {
			if !strings.Contains(prompt, wanted) {
				t.Errorf("prompt missing %q: %s", wanted, prompt)
			}
		}
		if request.MaxTokens != 1024 {
			t.Errorf("frozen maxTokens not applied: %d", request.MaxTokens)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "question-receipt", "model": "model-a", "usage": map[string]any{"prompt_tokens": 31, "completion_tokens": 17}, "choices": []any{map[string]any{"message": map[string]string{"content": `[{"content":"冷链须保持2～8℃，监测温度达到9℃时应如何隔离、排查并记录处置过程？","difficulty":"medium"}]`}}}})
	}))
	defer server.Close()
	blueprintID := int64(10)
	blueprint, _ := json.Marshal(map[string]any{"nodes": map[string]any{"generation": map[string]any{"sourceVersionId": 11}}})
	source, _ := json.Marshal(model.SourcePayload{SchemaVersion: "source.v1", Documents: []model.SourceDocumentEntry{{StableID: "cold", ChunkIDs: []int64{7}}}})
	versions := groundingVersions{10: {ProjectID: 1, Kind: model.KindBlueprint, Payload: blueprint}, 11: {ProjectID: 1, Kind: model.KindSource, Payload: source}}
	chunks := groundingChunks{{ID: 7, ProjectID: 1, HeadingPath: "冷链温控", Content: "冷链须保持2～8℃，异常温度时隔离货物并记录。"}}
	request := studio.UnitRequest{ProjectID: 1, BlueprintVersionID: &blueprintID, Unit: studio.PlannedUnit{DomainName: "冷链", DirectionName: "温控", Difficulty: "medium", Ordinal: 2, Source: model.SourceDocument, SourceChunkIDs: []int64{7}}}
	var receipts generationReceipts
	provider := llm.ProviderConfig{BaseURL: server.URL, APIKey: "test", Model: "model-a", MaxTokens: 1024, ObserveResponse: receipts.observe}
	question, materials, err := questionFor(context.Background(), provider, request, chunks, versions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(materials) != 1 || materials[0].ID != 7 || strings.Contains(question, "：第 ") {
		t.Fatalf("incorrect grounded result: %q %+v", question, materials)
	}
	usage := receipts.usage()
	if usage.InputTokens == nil || *usage.InputTokens != 31 || receipts.modelID != "model-a" {
		t.Fatalf("missing receipt: %+v", receipts)
	}
	request.Unit.SourceChunkIDs = []int64{8}
	if _, _, err := questionFor(context.Background(), provider, request, chunks, versions, nil); err == nil {
		t.Fatal("block added after frozen source must be rejected")
	}
	versions[11] = store.DocumentVersion{ProjectID: 2, Kind: model.KindSource, Payload: source}
	request.Unit.SourceChunkIDs = []int64{7}
	if _, _, err := questionFor(context.Background(), provider, request, chunks, versions, nil); err == nil {
		t.Fatal("cross-project source version must be rejected")
	}
	if calls != 1 {
		t.Fatalf("invalid source must not call provider; calls=%d", calls)
	}
}

func TestQuestionForRequiresExplicitSource(t *testing.T) {
	for _, source := range []string{"", model.SourceNone, model.SourceManual, model.SourceDocument} {
		request := studio.UnitRequest{Unit: studio.PlannedUnit{Source: source}}
		_, _, err := questionFor(context.Background(), llm.ProviderConfig{}, request, nil, nil, nil)
		if err == nil || classifyStudioJobError(err) != model.ErrorClassConfig {
			t.Fatalf("source %q must fail non-retryable config: %v", source, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `[{"content":"如何为仓储系统设计断电时的库存一致性校验与恢复流程？","difficulty":"medium"}]`}}}})
	}))
	defer server.Close()
	request := studio.UnitRequest{Unit: studio.PlannedUnit{Source: model.SourceAI, DirectionName: "库存一致性", Ordinal: 1}}
	question, materials, err := questionFor(context.Background(), llm.ProviderConfig{BaseURL: server.URL, APIKey: "test", Model: "test"}, request, nil, nil, nil)
	if err != nil || question == "" || len(materials) != 0 || questionSource(model.SourceAI) != "keyword_only" {
		t.Fatalf("explicit AI path failed: %q %v", question, err)
	}
}

func TestGenerationReceiptsKeepMissingUsageUnknown(t *testing.T) {
	input, output := int64(4), int64(6)
	var receipts generationReceipts
	receipts.observe(llm.ResponseMetadata{Usage: model.TokenUsage{InputTokens: &input, OutputTokens: &output}})
	receipts.observe(llm.ResponseMetadata{Usage: model.TokenUsage{InputTokens: &input}})
	usage := receipts.usage()
	if usage.InputTokens == nil || *usage.InputTokens != 8 || usage.OutputTokens != nil {
		t.Fatalf("unknown token amount must not become zero: %+v", usage)
	}
	var empty generationReceipts
	if empty.usage().HasAnyToken() {
		t.Fatal("no receipts must remain unknown")
	}
}
