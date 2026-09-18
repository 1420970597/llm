package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// 推理型模型（deepseek 系）会先流式输出 reasoning_content 增量，再输出 content 增量。
// 该样本来自真实 provider 抓包，用于锁定「思考不得混入正文」这一行为。
const realReasoningSSE = `data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"reasoning_content":"We"},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"reasoning_content":" need answer"},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"content":"[\"a\""},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"content":", \"b\"]"},"finish_reason":null,"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"role":"assistant"},"finish_reason":"stop","index":0}],"object":"chat.completion.chunk"}

data: [DONE]
`

func TestDecodeChatCompletionSSESeparatesReasoningFromContent(t *testing.T) {
	content, err := decodeChatCompletionSSE([]byte(realReasoningSSE))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if content != `["a", "b"]` {
		t.Fatalf("content must exclude reasoning, got %q", content)
	}
	if strings.Contains(content, "need answer") {
		t.Fatalf("reasoning leaked into content: %q", content)
	}
}

func TestDecodeChatCompletionSSEFallsBackToReasoningWhenNoContent(t *testing.T) {
	onlyReasoning := `data: {"choices":[{"delta":{"reasoning_content":"思考中"},"index":0}],"object":"chat.completion.chunk"}

data: [DONE]
`
	content, err := decodeChatCompletionSSE([]byte(onlyReasoning))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if content != "思考中" {
		t.Fatalf("expected reasoning fallback, got %q", content)
	}
}

func TestDecodeChatCompletionSSEWithoutContentReturnsError(t *testing.T) {
	metadataOnly := `data: {"choices":[],"object":"chat.completion.chunk"}

data: [DONE]
`
	if _, err := decodeChatCompletionSSE([]byte(metadataOnly)); err == nil {
		t.Fatal("expected error for metadata-only stream")
	}
}

func TestDecodeChatCompletionBodyReadsNonStreamContent(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"role":              "assistant",
				"content":           `["x","y"]`,
				"reasoning_content": "internal thinking",
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded chatCompletionResponse
	if err := decodeChatCompletionBody(body, &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got := decoded.Choices[0].Message.Content; got != `["x","y"]` {
		t.Fatalf("content mismatch: %q", got)
	}
}

func TestUnmarshalStructuredContentHandlesCodeFenceAndProse(t *testing.T) {
	var fenced []string
	if err := unmarshalStructuredContent("```json\n[\"甲\",\"乙\"]\n```", &fenced); err != nil {
		t.Fatalf("fenced decode failed: %v", err)
	}
	if len(fenced) != 2 || fenced[0] != "甲" {
		t.Fatalf("fenced result mismatch: %v", fenced)
	}

	var embedded []string
	if err := unmarshalStructuredContent("好的，结果如下：[\"丙\",\"丁\"] 以上。", &embedded); err != nil {
		t.Fatalf("embedded decode failed: %v", err)
	}
	if len(embedded) != 2 || embedded[1] != "丁" {
		t.Fatalf("embedded result mismatch: %v", embedded)
	}
}
