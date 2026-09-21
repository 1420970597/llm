package llm

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件验证 Issue #160 T07 的模型能力判定与用量提取。
//
// 为什么这些必须测：它们决定「请求发出去之前能不能拦住一定会失败的配置」
// 与「成本能不能被算出来」。两者的错误都不会抛异常 ——
// 前者表现为真实供应商 400（英文错误，用户无法行动），
// 后者表现为成本静默偏低（超支不报警）。

// TestBuiltinCapabilitiesKnowsTemperatureRejectingFamilies 覆盖 §5
// 「不一律允许 temperature」。
//
// 事实依据：推理型模型（gpt-5/o 系列/带 reasoner|thinking|r1 的模型）
// 接受 temperature 会直接 400。这不是保守估计，而是已知行为。
func TestBuiltinCapabilitiesKnowsTemperatureRejectingFamilies(t *testing.T) {
	rejecting := []string{
		"gpt-5", "gpt-5.1", "gpt-5-mini", "gpt-6-preview",
		"o1", "o1-preview", "o3-mini", "o4-mini",
		"deepseek-reasoner", "deepseek-r1", "qwq-thinking", "some-reasoning-model",
	}
	for _, modelName := range rejecting {
		caps := BuiltinCapabilities("openai-compatible", modelName)
		if caps.SupportsTemperature {
			t.Fatalf("模型 %s 属于拒绝 temperature 的家族，必须声明不支持", modelName)
		}
		if !caps.SupportsReasoningEffort {
			t.Fatalf("模型 %s 支持 reasoning_effort，应声明支持", modelName)
		}
	}

	accepting := []string{"gpt-4o", "gpt-4.1", "deepseek-chat", "qwen-plus", "claude-sonnet-4"}
	for _, modelName := range accepting {
		caps := BuiltinCapabilities("openai-compatible", modelName)
		if !caps.SupportsTemperature {
			t.Fatalf("模型 %s 支持 temperature，不该被误拒", modelName)
		}
	}
}

// TestBuiltinCapabilitiesNeverInventsOutputLimit 覆盖
// 「不猜上限」这一取舍。
//
// 猜一个上限的两种错法代价不对称：猜小了会把合法配置**误拒**（用户无法绕过），
// 猜大了等于没校验。因此内置默认一律不声明（0=未声明），
// 真实上限由管理员在连接设置里声明。
func TestBuiltinCapabilitiesNeverInventsOutputLimit(t *testing.T) {
	for _, modelName := range []string{"gpt-5.1", "gpt-4o", "deepseek-chat", "unknown-model"} {
		caps := BuiltinCapabilities("openai-compatible", modelName)
		if caps.MaxOutputTokens != 0 || caps.MaxContextTokens != 0 {
			t.Fatalf("模型 %s 的内置默认不得猜测 token 上限，实际 %d/%d",
				modelName, caps.MaxOutputTokens, caps.MaxContextTokens)
		}
		if caps.Source != model.CapabilitySourceBuiltin {
			t.Fatalf("内置默认必须标明来源为 builtin-default，实际 %s", caps.Source)
		}
	}
}

// TestValidateModelRequestBlocksImpossibleRequests 覆盖
// 「把一定会失败的请求拦在花钱之前」。
func TestValidateModelRequestBlocksImpossibleRequests(t *testing.T) {
	caps := BuiltinCapabilities("openai-compatible", "gpt-5.1")

	errs := ValidateModelRequest(caps, model.ModelRequestSpec{
		ModelName: "gpt-5.1", Temperature: floatPointerHelper(0.7),
	})
	if len(errs) == 0 {
		t.Fatal("给不支持 temperature 的模型传 temperature 必须被拦住")
	}
	if errs[0].Field == "" || errs[0].Message == "" {
		t.Fatalf("字段错误必须能直接展示给用户，实际 %+v", errs[0])
	}

	// 合法请求不得被拦（否则能力声明会变成「什么都不能用」）。
	if errs := ValidateModelRequest(caps, model.ModelRequestSpec{
		ModelName: "gpt-5.1", ReasoningEffort: "medium", MaxOutputTokens: 4096, StructuredOutput: true,
	}); len(errs) != 0 {
		t.Fatalf("合法请求不该被拦，实际 %v", errs)
	}
}

// TestRequestConfigFingerprintIsStableAndDiscriminating 覆盖 config 指纹。
//
// 两条性质缺一不可：
//   - **稳定**：同一配置反复计算结果相同（否则成本对比失去意义）；
//   - **可区分**：任何影响成本或产出的参数变化都必须改变指纹。
//     浮点温度的表示差异（0.1 vs 0.10000000000000001）不得造成误区分。
func TestRequestConfigFingerprintIsStableAndDiscriminating(t *testing.T) {
	base := model.ModelRequestSpec{
		EndpointURL: "https://api.example.com/v1", ModelName: "m",
		Temperature: floatPointerHelper(0.1), MaxOutputTokens: 4096, SchemaVersion: "sft.sample.v1",
	}
	first := RequestConfigFingerprint(base)
	if again := RequestConfigFingerprint(base); again != first {
		t.Fatalf("同配置必须得到同指纹：%s vs %s", first, again)
	}
	if first == "" || !strings.HasPrefix(first, "cfg:") {
		t.Fatalf("指纹格式异常：%q", first)
	}

	mutations := map[string]model.ModelRequestSpec{
		"温度":     withTemperature(base, 0.2),
		"输出上限":   withMaxTokens(base, 8192),
		"模型名":    withModelName(base, "m2"),
		"接入点":    withEndpoint(base, "https://other.example.com/v1"),
		"schema": withSchema(base, "grpo.sample.v1"),
		"思考强度":   withReasoningEffort(base, "high"),
	}
	for name, mutated := range mutations {
		if RequestConfigFingerprint(mutated) == first {
			t.Fatalf("改动「%s」后指纹必须变化（否则不同配置的账目会混在一起）", name)
		}
	}

	// 大小写/首尾空格不该造成不同指纹（配置项的规范化差异不是真正的变化）。
	normalized := withModelName(base, "  M  ")
	if RequestConfigFingerprint(normalized) != first {
		t.Fatal("模型名大小写与首尾空格差异不得产生不同指纹")
	}

	// 同一接入点的末尾斜杠与 scheme 差异同样不该区分。
	slash := withEndpoint(base, "http://api.example.com/v1/")
	if RequestConfigFingerprint(slash) != first {
		t.Fatal("接入点末尾斜杠/scheme 差异不得产生不同指纹")
	}
}

// TestExtractUsageFromBodyNeverInventsZero 覆盖 T07 最硬的一条：
// **未知不是 0**。
//
// 三种形态都必须返回 nil 而不是 0：
//   - 响应体里根本没有 usage；
//   - usage 是空对象；
//   - 响应体无法解析。
func TestExtractUsageFromBodyNeverInventsZero(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"没有 usage 字段", `{"id":"r1","model":"m","choices":[{"message":{"content":"hi"}}]}`},
		{"usage 是空对象", `{"id":"r1","model":"m","usage":{},"choices":[{"message":{"content":"hi"}}]}`},
		{"无法解析", `not json`},
		{"空响应", ``},
		{"流式但没有 usage 事件", "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\ndata: [DONE]\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			usage := ExtractUsageFromBody([]byte(testCase.body))
			if usage.InputTokens != nil || usage.OutputTokens != nil {
				t.Fatalf("不得编造 token 数（未知必须保持 nil），实际 input=%v output=%v",
					usage.InputTokens, usage.OutputTokens)
			}
			if usage.Source != model.UsageSourceUnknown {
				t.Fatalf("来源必须是 unknown，实际 %s", usage.Source)
			}
			if usage.HasAnyToken() {
				t.Fatal("HasAnyToken 必须为 false")
			}
		})
	}
}

// TestExtractUsageFromBodyReadsNonStreamingAndStreaming 覆盖两种响应形态。
//
// 请求默认是流式的（`stream: true`），而流式响应的 usage 在最后一个事件里；
// 普通 JSON 反序列化取不到它。因此这里两种都要覆盖。
func TestExtractUsageFromBodyReadsNonStreamingAndStreaming(t *testing.T) {
	nonStreaming := `{"id":"req-1","model":"m","usage":{"prompt_tokens":120,"completion_tokens":340},
    "choices":[{"message":{"content":"hi"}}]}`
	usage := ExtractUsageFromBody([]byte(nonStreaming))
	if usage.InputTokens == nil || *usage.InputTokens != 120 {
		t.Fatalf("非流式响应必须能取到输入 token，实际 %v", usage.InputTokens)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 340 {
		t.Fatalf("非流式响应必须能取到输出 token，实际 %v", usage.OutputTokens)
	}
	if usage.Source != model.UsageSourceProvider {
		t.Fatalf("来源必须是 provider，实际 %s", usage.Source)
	}

	streaming := strings.Join([]string{
		`data: {"id":"req-2","model":"m","choices":[{"delta":{"content":"a"}}]}`,
		``,
		`data: {"id":"req-2","model":"m","choices":[{"delta":{"content":"b"}}]}`,
		``,
		`data: {"id":"req-2","model":"m","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":22}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	streamed := ExtractUsageFromBody([]byte(streaming))
	if streamed.InputTokens == nil || *streamed.InputTokens != 11 {
		t.Fatalf("流式响应必须能取到最后一个 usage 事件的输入 token，实际 %v", streamed.InputTokens)
	}
	if streamed.OutputTokens == nil || *streamed.OutputTokens != 22 {
		t.Fatalf("流式响应必须能取到输出 token，实际 %v", streamed.OutputTokens)
	}

	// 部分供应商用 input_tokens/output_tokens 命名。
	alternate := `{"usage":{"input_tokens":7,"output_tokens":9}}`
	alias := ExtractUsageFromBody([]byte(alternate))
	if alias.InputTokens == nil || *alias.InputTokens != 7 || alias.OutputTokens == nil || *alias.OutputTokens != 9 {
		t.Fatalf("必须容忍 input_tokens/output_tokens 命名，实际 %+v", alias)
	}

	// 只有一个方向已知时，另一个方向保持 nil（不得补 0 —— 补 0 会把
	// 该方向的成本算成免费）。
	partial := `{"usage":{"completion_tokens":50}}`
	partialUsage := ExtractUsageFromBody([]byte(partial))
	if partialUsage.InputTokens != nil {
		t.Fatalf("未回传的方向必须保持 nil，实际 %v", partialUsage.InputTokens)
	}
	if partialUsage.OutputTokens == nil || *partialUsage.OutputTokens != 50 {
		t.Fatalf("已回传的方向必须保留，实际 %v", partialUsage.OutputTokens)
	}
}

// TestExtractIdentityFromBody 覆盖「请求 ID 与响应模型」的取证。
//
// response_model_id 是「请求的别名被路由到了哪个真实模型」的第一手证据，
// 也是 T07 验收项「撤销连接时不偷偷换模型」的取证面。
func TestExtractIdentityFromBody(t *testing.T) {
	nonStreaming := `{"id":"req-9","model":"m-2026-09-01","choices":[{"message":{"content":"x"}}]}`
	requestID, responseModel := ExtractIdentityFromBody([]byte(nonStreaming))
	if requestID != "req-9" || responseModel != "m-2026-09-01" {
		t.Fatalf("非流式身份提取失败：%q %q", requestID, responseModel)
	}

	streaming := strings.Join([]string{
		`data: {"id":"req-10","model":"m-alias"}`,
		``,
		`data: {"id":"req-10","model":"m-real"}`,
		``,
		`data: [DONE]`,
	}, "\n")
	requestID, responseModel = ExtractIdentityFromBody([]byte(streaming))
	if requestID != "req-10" {
		t.Fatalf("流式请求 ID 提取失败：%q", requestID)
	}
	if responseModel != "m-real" {
		t.Fatalf("必须保留最新的响应模型标识（路由后的真实模型），实际 %q", responseModel)
	}

	if requestID, responseModel := ExtractIdentityFromBody([]byte(``)); requestID != "" || responseModel != "" {
		t.Fatalf("空响应不得编造身份：%q %q", requestID, responseModel)
	}
}

// TestWithUsageReportingOnlyAffectsStreamingRequests 覆盖
// 「不改变现有请求载荷」这一兼容性要求。
//
// 部分 OpenAI 兼容接入点会拒绝不认识的字段（400），因此
// stream_options 只能在显式开启时、且仅在流式请求上出现。
func TestWithUsageReportingOnlyAffectsStreamingRequests(t *testing.T) {
	payload := map[string]any{"model": "m"}

	// 默认关闭：载荷里不得出现 stream_options。
	offBodies, err := buildChatCompletionBodies(ProviderConfig{Model: "gpt-4o"}, payload)
	if err != nil {
		t.Fatalf("buildChatCompletionBodies: %v", err)
	}
	if strings.Contains(string(offBodies[0]), "stream_options") {
		t.Fatal("未显式开启时不得向供应商发送 stream_options（部分接入点会 400）")
	}

	// 显式开启且流式：必须出现 include_usage。
	onBodies, err := buildChatCompletionBodies(WithUsageReporting(ProviderConfig{Model: "gpt-4o"}), payload)
	if err != nil {
		t.Fatalf("buildChatCompletionBodies: %v", err)
	}
	if !strings.Contains(string(onBodies[0]), "include_usage") {
		t.Fatalf("显式开启后必须要求用量回传，实际 %s", onBodies[0])
	}

	// 非流式请求不该出现 stream_options（它没有意义，且可能被拒）。
	nonStreaming := map[string]any{"model": "m", "stream": false}
	bodies, err := buildChatCompletionBodies(WithUsageReporting(ProviderConfig{Model: "m"}), nonStreaming)
	if err != nil {
		t.Fatalf("buildChatCompletionBodies: %v", err)
	}
	if strings.Contains(string(bodies[0]), "stream_options") {
		t.Fatalf("非流式请求不得携带 stream_options，实际 %s", bodies[0])
	}
}

func withTemperature(spec model.ModelRequestSpec, value float64) model.ModelRequestSpec {
	spec.Temperature = floatPointerHelper(value)
	return spec
}

func withMaxTokens(spec model.ModelRequestSpec, value int) model.ModelRequestSpec {
	spec.MaxOutputTokens = value
	return spec
}

func withModelName(spec model.ModelRequestSpec, value string) model.ModelRequestSpec {
	spec.ModelName = value
	return spec
}

func withEndpoint(spec model.ModelRequestSpec, value string) model.ModelRequestSpec {
	spec.EndpointURL = value
	return spec
}

func withSchema(spec model.ModelRequestSpec, value string) model.ModelRequestSpec {
	spec.SchemaVersion = value
	return spec
}

func withReasoningEffort(spec model.ModelRequestSpec, value string) model.ModelRequestSpec {
	spec.ReasoningEffort = value
	return spec
}

func floatPointerHelper(value float64) *float64 { return &value }
