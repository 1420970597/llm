//go:build integration

// R3 真实 provider 端到端验证：确认重新标定后的阈值不会误伤真实模型输出。
//
// 为什么需要它：单元测试用的是手写样本；本用例直接调真实 provider
// （http://152.53.126.151:8885/v1，model global:deepseek-v4.1-flash），
// 证明「真实输出 → generated、占位输出 → invalid」这条判定在真实链路上成立。
//
// 跑法（在宿主机，容器只需能访问 provider）：
//
//	R3_API_KEY=<provider 明文 key> \
//	docker run --rm -v <worktree>:/w -w /w \
//	  -e R3_API_KEY -e GOFLAGS=-mod=readonly \
//	  golang:1.24-alpine sh -c "go test -tags=integration -run TestRealProvider -v ./test/integration/"
//
// 缺少 R3_API_KEY 时用例 skip 并提示「输入缺失」，不伪造通过。
package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
)

const (
	realProviderBaseURL = "http://152.53.126.151:8885/v1"
	realProviderModel   = "global:deepseek-v4.1-flash"
)

// TestRealProviderReasoningAndRewardAreAccepted 用真实 provider 跑推理与评分生成，
// 断言结果状态是 generated（而不是被新阈值误判成 invalid）。
func TestRealProviderReasoningAndRewardAreAccepted(t *testing.T) {
	apiKey := os.Getenv("R3_API_KEY")
	if apiKey == "" {
		t.Skip("输入缺失：未提供 R3_API_KEY，跳过真实 LLM 端到端验证（不伪造通过）")
	}

	provider := llm.ProviderConfig{
		BaseURL:      realProviderBaseURL,
		Model:        realProviderModel,
		ProviderType: "openai-compatible",
		APIKey:       apiKey,
	}
	dataset := model.Dataset{ID: 1, RootKeyword: "军事"}
	question := model.Question{
		ID:      1,
		Content: "在某某海域有某某编队执行巡逻任务，突然探测到不明水下目标，请做出规划。",
	}

	t.Run("reasoning", func(t *testing.T) {
		records, _, err := llm.GenerateReasoning(context.Background(), provider, dataset,
			[]model.Question{question}, nil)
		if err != nil {
			t.Fatalf("GenerateReasoning 返回错误: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("应产出 1 条记录，得到 %d", len(records))
		}
		record := records[0]
		t.Logf("真实输出：status=%s reasoning_runes=%d answer_runes=%d",
			record.Status, len([]rune(record.Reasoning)), len([]rune(record.AnswerSummary)))
		if record.Status != "generated" {
			t.Fatalf("真实模型输出被判为 %q 而非 generated —— 阈值误伤真实数据", record.Status)
		}
	})

	t.Run("reward", func(t *testing.T) {
		records, _, err := llm.GenerateRewards(context.Background(), provider, dataset,
			[]model.Question{question}, nil)
		if err != nil {
			t.Fatalf("GenerateRewards 返回错误: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("应产出 1 条记录，得到 %d", len(records))
		}
		t.Logf("真实输出：status=%s score=%v", records[0].Status, records[0].Score)
		if records[0].Status != "generated" {
			t.Fatalf("真实模型评分理由被判为 %q —— 阈值误伤真实数据", records[0].Status)
		}
	})
}

// TestPlaceholderIsRejectedEndToEnd 用假的 OpenAI 兼容服务返回 issue #7 的
// 故障形状（合法 JSON + 占位内容），走完整 HTTP → 解码 → 校验链路，
// 证明它会被判为 invalid 而不是 generated。
//
// 与上面的真实 provider 用例互补：真实 provider 无法稳定复现「模型摆烂」，
// 因此用假服务精确复现故障形状。
func TestPlaceholderIsRejectedEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"answer\":\"...\",\"reasoning\":\"...\"}"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	provider := llm.ProviderConfig{
		BaseURL:      server.URL,
		Model:        realProviderModel,
		ProviderType: "openai-compatible",
		APIKey:       "test-key",
	}
	dataset := model.Dataset{ID: 1, RootKeyword: "军事"}
	question := model.Question{ID: 1, Content: "在某某海域有某某编队执行巡逻任务，请做出规划。"}

	records, _, err := llm.GenerateReasoning(context.Background(), provider, dataset,
		[]model.Question{question}, nil)
	if err != nil {
		t.Fatalf("单条不合格不应返回整批错误: %v", err)
	}
	if len(records) != 1 || records[0].Status != llm.ContentStatusInvalid {
		t.Fatalf("issue #7 的占位故障形状必须判为 %q，实际 %+v", llm.ContentStatusInvalid, records)
	}
}
