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

// The live canonical-model endpoint emits reasoning_details[].text. Those
// events fall through the generic SSE reader and must remain separate from
// the final JSON, including when an internal format example is itself JSON.
func TestDecodeChatCompletionSSESeparatesStructuredReasoningFromContent(t *testing.T) {
	raw := `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"思考示例：[{\"content\":\"问题正文\",\"difficulty\":\"medium\"}]","model":"deepseek-v4.1-flash","id":"reasoning-1"}]},"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"content":"[{\"content\":\"仓库应如何核查","reasoning_details":[{"text":"还在内部思考","id":"reasoning-2"}]},"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{"content":"冷链温控异常？\",\"difficulty\":\"medium\"}]"},"index":0}],"object":"chat.completion.chunk"}

data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"object":"chat.completion.chunk"}

data: [DONE]
`
	content, err := decodeChatCompletionSSE([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"content":"仓库应如何核查冷链温控异常？","difficulty":"medium"}]`
	if content != want {
		t.Fatalf("structured reasoning leaked into final JSON: %q", content)
	}
	drafts, err := parseQuestionDrafts(content)
	if err != nil || len(drafts) != 1 || drafts[0].Content != "仓库应如何核查冷链温控异常？" {
		t.Fatalf("final questions=%v, err=%v", drafts, err)
	}
}

func TestDecodeChatCompletionSSEStructuredReasoningAloneIsNotFinalContent(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
	}{
		{"ordered-reasoning-only", `[{"type":"reasoning.text","text":"依据素材，"},{"type":"reasoning.text","text":"核对异常时限。"}]`},
		{"encrypted-details-are-not-text", `[{"type":"reasoning.encrypted","data":"opaque-vendor-token"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := `data: {"choices":[{"delta":{"reasoning_details":` + test.data + `},"index":0}],"object":"chat.completion.chunk"}` + "\n\ndata: [DONE]\n"
			content, err := decodeChatCompletionSSE([]byte(raw))
			if err == nil {
				t.Fatalf("internal reasoning metadata became final content: %q", content)
			}
		})
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

func TestDecodeWrappedChatCompletionKeepsReasoningExamplesOutOfContent(t *testing.T) {
	const content = `{"chainOfThought":"依据素材核对退货窗口与冷链例外。","answer":"冷链异常需独立审核。"}`
	const example = `思考输出格式：{"chainOfThought":"第1步... \\n第2步...","answer":"..."}`
	for _, test := range []struct {
		name string
		data map[string]any
		want string
	}{
		{"wrapped-choices", map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"content": content, "reasoning_content": example,
		}}}}, content},
		{"wrapped-output-text", map[string]any{"output_text": content, "reasoning": example}, content},
		{"structured-reasoning-is-separate", map[string]any{"output_text": content, "reasoning": map[string]any{"text": example}}, content},
		{"structured-reasoning-details-are-separate", map[string]any{"output_text": content, "reasoning_details": []any{map[string]any{"text": example}}}, content},
		{"reasoning-only-fallback", map[string]any{"content": "", "reasoning_content": "只有推理正文"}, "只有推理正文"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"success": true, "data": test.data})
			if err != nil {
				t.Fatal(err)
			}
			// The former map traversal made this intermittent: repeated decoding
			// must always retain only the final content when it is present.
			for i := 0; i < 50; i++ {
				var decoded chatCompletionResponse
				if err := decodeChatCompletionBody(body, &decoded); err != nil {
					t.Fatal(err)
				}
				if got := decoded.Choices[0].Message.Content; got != test.want {
					t.Fatalf("wrapped response content=%q, want=%q", got, test.want)
				}
			}
		})
	}
}

func TestDecodeWrappedChatCompletionRejectsEmptyContent(t *testing.T) {
	for _, body := range []string{
		`{"success":true,"data":{"choices":[{"message":{"content":"","reasoning_content":""}}]}}`,
		`{"success":true,"data":{"choices":[{"message":{"content":"","reasoning_details":[{"text":"内部推理 JSON 示例：{\"answer\":\"...\"}"}]}}]}}`,
	} {
		var decoded chatCompletionResponse
		if err := decodeChatCompletionBody([]byte(body), &decoded); err == nil {
			t.Fatal("metadata-only wrapper must not become a successful model response")
		}
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

func TestBuildChatCompletionBodiesUsesGPT5OutputField(t *testing.T) {
	bodies, err := buildChatCompletionBodies(ProviderConfig{Model: "gpt-5.1", MaxTokens: 1234}, map[string]any{"messages": []any{}})
	if err != nil || len(bodies) == 0 {
		t.Fatalf("build gpt-5 request: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body["max_completion_tokens"] != float64(1234) {
		t.Fatalf("gpt-5 output field missing: %s", bodies[0])
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("gpt-5 primary variant sent max_tokens: %s", bodies[0])
	}
}

func TestBuildChatCompletionBodiesKeepsLegacyOutputFieldForOtherModels(t *testing.T) {
	bodies, err := buildChatCompletionBodies(ProviderConfig{Model: "gpt-4o", MaxTokens: 1234}, map[string]any{"messages": []any{}})
	if err != nil || len(bodies) != 1 {
		t.Fatalf("build legacy request: %v bodies=%d", err, len(bodies))
	}
	var body map[string]any
	if err := json.Unmarshal(bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if body["max_tokens"] != float64(1234) {
		t.Fatalf("legacy output field missing: %s", bodies[0])
	}
}
