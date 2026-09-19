package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCompleteStructuredRetriesNonJSONResponse 锁定裁判调用的重试行为。
//
// 背景（父代理用真实端到端验收发现的缺陷）：推理型裁判模型有时会把**思考过程**
// 当成正文返回（实测 48 次调用里 6 次如此，12.5%），内容形如
// 「我们需要评估的维度是"表达精炼度"…」而不是 JSON。
// 而 CompleteStructured 此前**只尝试一次**，解析失败即把该条记为 failed。
//
// 后果不是「偶发失败可忽略」，而是**评分覆盖率下降**：那次验收 48 条里有 6 条没有
// 分数，整个运行被判为 partial_failed，报告的结论退化为
// 「此时不给出质量结论 —— 部分评分不足以代表整个数据集」。
//
// 本测试断言：
//  1. 首次返回非 JSON、第二次返回 JSON 时，调用**成功**（即确实重试了）；
//  2. 重试时带上了**纠正指令**（不是把同一提示词原样再发一遍）；
//  3. 一直返回非 JSON 时，最终报错且错误里含尝试次数（不无限重试）。
func TestCompleteStructuredRetriesNonJSONResponse(t *testing.T) {
	const proseResponse = "我们需要评估的维度是“表达精炼度”，描述是：在保持完整的前提下，表达是否紧凑。"
	const jsonResponse = `{"score": 3, "rationale": "表达较为紧凑，无冗余铺垫。"}`

	var (
		calls       int
		secondUser  string
		firstUser   string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, message := range body.Messages {
			if message.Role != "user" {
				continue
			}
			if calls == 1 {
				firstUser = message.Content
			} else if calls == 2 {
				secondUser = message.Content
			}
		}

		content := proseResponse
		if calls >= 2 {
			content = jsonResponse
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": content},
			}},
		})
	}))
	defer server.Close()

	provider := ProviderConfig{
		BaseURL:      server.URL,
		Model:        "test-judge",
		ProviderType: "openai-compatible",
		APIKey:       "test-key",
	}

	var target struct {
		Score     float64 `json:"score"`
		Rationale string  `json:"rationale"`
	}
	err := CompleteStructured(context.Background(), provider,
		"你只输出一个 JSON 对象。", "请给这一条打分。", &target, 10*time.Second)

	if err != nil {
		t.Fatalf("首次非 JSON、二次 JSON 时应重试成功，实际报错: %v", err)
	}
	if calls != 2 {
		t.Fatalf("应恰好调用 2 次（1 次失败 + 1 次重试），实际 %d 次", calls)
	}
	if target.Score != 3 {
		t.Errorf("解析结果应写入 target，实际 score=%v", target.Score)
	}

	// 断言重试带上了纠正指令：只把同样的提示词再发一次，往往得到同样的跑偏结果。
	if !strings.Contains(secondUser, "上一次") || !strings.Contains(secondUser, "JSON") {
		t.Errorf("重试的提示词应包含明确的纠正指令（指出上次不是 JSON），实际:\n%s", secondUser)
	}
	if firstUser == secondUser {
		t.Errorf("重试不应原样重复第一次的提示词")
	}
	// 纠正指令必须是**追加**，不能丢掉原始任务描述。
	if !strings.Contains(secondUser, "请给这一条打分。") {
		t.Errorf("重试的提示词必须保留原始任务描述，实际:\n%s", secondUser)
	}
}

// TestCompleteStructuredGivesUpAfterAttempts 断言不会无限重试。
//
// 若模型**结构性**不支持 JSON 输出（例如把思考过程当正文是它的固有行为），
// 必须有界地放弃并把尝试次数写进错误，否则一次评估会被一个坏模型拖死。
func TestCompleteStructuredGivesUpAfterAttempts(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "这不是 JSON，只是解释性文字。"},
			}},
		})
	}))
	defer server.Close()

	provider := ProviderConfig{
		BaseURL:      server.URL,
		Model:        "bad-judge",
		ProviderType: "openai-compatible",
		APIKey:       "test-key",
	}

	var target struct {
		Score float64 `json:"score"`
	}
	err := CompleteStructured(context.Background(), provider,
		"你只输出 JSON。", "请打分。", &target, 10*time.Second)
	if err == nil {
		t.Fatalf("一直返回非 JSON 时必须报错")
	}
	if calls != judgeJSONAttempts {
		t.Errorf("应恰好尝试 %d 次后放弃，实际 %d 次", judgeJSONAttempts, calls)
	}
	if !strings.Contains(err.Error(), "non-JSON") {
		t.Errorf("错误信息应说明是「非 JSON」问题，便于排查，实际: %v", err)
	}
}

// TestCompleteStructuredDoesNotRetryTransportFailure 断言传输层失败不叠加重试。
//
// requestChatCompletion 内部已有自己的重试与退避；若这里再套一层，
// 两层相乘会让单条评分的耗时失控（评估本身已接近一小时）。
func TestCompleteStructuredDoesNotRetryTransportFailure(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	provider := ProviderConfig{
		BaseURL:      server.URL,
		Model:        "flaky-judge",
		ProviderType: "openai-compatible",
		APIKey:       "test-key",
	}

	var target struct {
		Score float64 `json:"score"`
	}
	err := CompleteStructured(context.Background(), provider, "sys", "user", &target, 10*time.Second)
	if err == nil {
		t.Fatalf("传输失败必须报错")
	}
	// requestChatCompletion 自己会试若干次（buildChatCompletionBodies 的变体数），
	// 这里只断言「没有额外的 judgeJSONAttempts 倍放大」：调用次数应远小于
	// judgeJSONAttempts × 变体数。用 judgeJSONAttempts*2 作为宽松上界。
	if calls >= judgeJSONAttempts*2 {
		t.Errorf("传输层失败不应再叠加结构化重试（两层相乘会让耗时失控），实际调用 %d 次", calls)
	}
}
