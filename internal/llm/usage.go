package llm

import (
	"encoding/json"
	"strings"

	"github.com/1420970597/llm/internal/model"
)

// 本文件实现「从供应商响应里取回用量元数据」（Issue #160 T07）。
//
// 契约：§2.4（token 用量与费用四态）、§4.1（Usage 对象）。
//
// 三条实现约定：
//
//  1. **只报告看到的事实**。供应商没回传 usage 时返回
//     `UsageSource: unknown` 且两个 token 字段都是 nil —— 不是 0。
//     0 与未知的区别在整个成本链路里都是关键的（见 internal/model/usage.go）。
//  2. **流式响应也要能取到用量**。现在的调用链默认 `stream: true`，
//     而流式响应的 usage 出现在最后一个 data 事件里，普通 JSON 反序列化
//     取不到它。因此除了解析顶层 usage，还额外扫描 SSE 事件。
//  3. **不改变现有请求载荷**。让供应商回传 usage 需要 `stream_options.
//     include_usage`，而部分接入点会拒绝不认识的字段。因此这个开关
//     由调用方通过 ProviderConfig.IncludeUsage 显式打开（默认关闭）。

// ExtractResponseUsage 从已解码的响应中取回用量。
//
// 语义：只要有一个方向的 token 数存在就算拿到了用量，另一个方向保持 nil
// （不补 0）。补 0 会把该方向的成本算成免费。
func ExtractResponseUsage(resp chatCompletionResponse) model.TokenUsage {
	usage := model.TokenUsage{Source: model.UsageSourceUnknown}
	if resp.Usage.PromptTokens != nil {
		usage.InputTokens = resp.Usage.PromptTokens
	}
	if resp.Usage.CompletionTokens != nil {
		usage.OutputTokens = resp.Usage.CompletionTokens
	}
	if usage.HasAnyToken() {
		usage.Source = model.UsageSourceProvider
	}
	return usage
}

// ExtractUsageFromBody 从**原始响应体**中取回用量。
//
// 存在的理由：请求默认是流式的，而流式响应的 body 是一串 `data: {...}` 事件。
// decodeChatCompletionBody 会把内容拼成正文，但拼正文的过程中 usage 事件
// （只有 usage、没有 choices）会被当成「非内容事件」丢掉。因此这里独立扫描
// 一份，最后一个带 usage 的事件胜出（供应商的约定是最后发用量汇总）。
//
// 非流式 JSON 直接命中第一种情况。两种都取不到时返回 unknown。
func ExtractUsageFromBody(raw []byte) model.TokenUsage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return model.TokenUsage{Source: model.UsageSourceUnknown}
	}

	// 非流式：整段就是一个 JSON 对象。
	//
	// 用宽松解析器（extractUsageFromJSONPayload）而不是严格的
	// chatCompletionResponse：后者只认 prompt_tokens/completion_tokens，
	// 而真实接入点还有 input_tokens/output_tokens 的写法。
	// 严格解析会让这些供应商的用量永远显示为「未知」，进而让成本无法计算。
	if strings.HasPrefix(trimmed, "{") {
		if usage := extractUsageFromJSONPayload(trimmed); usage.HasAnyToken() {
			return usage
		}
	}

	// 流式：逐行扫描 data: 事件，取最后一个带 usage 的。
	usage := model.TokenUsage{Source: model.UsageSourceUnknown}
	for _, line := range strings.Split(trimmed, "\n") {
		payload := strings.TrimSpace(line)
		if !strings.HasPrefix(payload, "data:") {
			continue
		}
		payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		candidate := extractUsageFromJSONPayload(payload)
		if candidate.HasAnyToken() {
			usage = candidate
		}
	}
	return usage
}

// extractUsageFromJSONPayload 从一个 JSON 事件里取 usage。
//
// 用**宽松**结构而不是复用 chatCompletionResponse：真实流式事件里
// usage 字段的形态各供应商略有差异（有的把 prompt_tokens 放在
// input_tokens 里），而这里只需要数字，多容忍几种写法比严格拒绝更有用。
// 但绝不容忍「缺失即 0」——缺失就是 nil。
func extractUsageFromJSONPayload(payload string) model.TokenUsage {
	var probe struct {
		Usage *struct {
			PromptTokens     *int64 `json:"prompt_tokens"`
			CompletionTokens *int64 `json:"completion_tokens"`
			InputTokens      *int64 `json:"input_tokens"`
			OutputTokens     *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &probe); err != nil || probe.Usage == nil {
		return model.TokenUsage{Source: model.UsageSourceUnknown}
	}

	usage := model.TokenUsage{Source: model.UsageSourceUnknown}
	usage.InputTokens = firstNonNil(probe.Usage.PromptTokens, probe.Usage.InputTokens)
	usage.OutputTokens = firstNonNil(probe.Usage.CompletionTokens, probe.Usage.OutputTokens)
	if usage.HasAnyToken() {
		usage.Source = model.UsageSourceProvider
	}
	return usage
}

// firstNonNil 返回第一个非 nil 的指针。
func firstNonNil(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// ExtractResponseIdentity 取回「这次调用是哪个请求、哪个模型」。
//
// 两者都用于与供应商账单核对（§2.4「核对供应商账单的状态」）：
//   - request_id 是账单明细的关联键；
//   - response_model_id 揭示「请求的别名被路由到了哪个真实模型」——
//     这是「撤销连接后是不是偷偷换了模型」的第一手证据。
func ExtractResponseIdentity(resp chatCompletionResponse) (requestID, responseModelID string) {
	return strings.TrimSpace(resp.ID), strings.TrimSpace(resp.Model)
}

// ExtractIdentityFromBody 从原始响应体里取请求 ID 与响应模型标识。
func ExtractIdentityFromBody(raw []byte) (requestID, responseModelID string) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", ""
	}
	if strings.HasPrefix(trimmed, "{") {
		var resp chatCompletionResponse
		if err := json.Unmarshal([]byte(trimmed), &resp); err == nil {
			return ExtractResponseIdentity(resp)
		}
	}
	// 流式：取最后一个带 id/model 的事件（若首尾都有，末尾的更新）。
	for _, line := range strings.Split(trimmed, "\n") {
		payload := strings.TrimSpace(line)
		if !strings.HasPrefix(payload, "data:") {
			continue
		}
		payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var probe struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(payload), &probe); err != nil {
			continue
		}
		if probe.ID != "" {
			requestID = strings.TrimSpace(probe.ID)
		}
		if probe.Model != "" {
			responseModelID = strings.TrimSpace(probe.Model)
		}
	}
	return requestID, responseModelID
}

// WithUsageReporting 打开「要求供应商在流式响应里回传用量」。
//
// 为什么要显式开关（而不是一律加上 `stream_options.include_usage`）：
// 部分 OpenAI 兼容接入点会拒绝不认识的字段（400 invalid_request_error），
// 一律加上会让原本可用的部署直接失败。因此它必须是显式选择，
// 且只在新的 Studio 生成路径（T12）里启用 —— 旧路径行为保持不变。
func WithUsageReporting(provider ProviderConfig) ProviderConfig {
	provider.IncludeUsage = true
	return provider
}
