package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
)

// 本文件实现模型能力声明与请求校验（Issue #160 T07，契约 §5）。
//
// §5 原文：「模型参数默认取**能力声明**，不一律允许 temperature（见 T07）」。
//
// 为什么需要它：现有实现把所有参数一律往 payload 里塞（`applyReasoningEffort`
// 与 `buildChatCompletionBodies` 里的 gpt-5 特判就是补丁的痕迹）。真实后果是
// 「配置看起来保存成功、一运行就是供应商 400」，而 400 的文案是英文的
// 供应商错误，用户无法据此行动。能力校验把「一定会失败」的请求拦在
// **花钱之前**，并给出中文字段级错误。
//
// 分层：
//   - 数据库里管理员声明的能力（model_capabilities，T28 的界面维护）优先；
//   - 没有声明时回退到 BuiltinCapabilities（按模型家族的保守默认），
//     并在 ModelCapabilities.Source 里标明来源，界面必须能区分两者。

// BuiltinCapabilities 在没有显式声明时给出保守的默认能力。
//
// 保守的含义是「按已知事实收敛，不按猜想放宽」：
//   - 已知**拒绝** temperature 的模型家族一律声明不支持，因为传了必然 400；
//   - 输出上限**不猜**（保持 0=未声明）。猜一个上限的两种错法代价不对称：
//     猜小了会把合法配置误拒（用户无法绕过），猜大了等于没校验。
//     真实上限应由管理员在连接设置里声明。
func BuiltinCapabilities(providerType, modelName string) model.ModelCapabilities {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	caps := model.ModelCapabilities{
		ModelName: modelName,
		// 默认支持 temperature / JSON 模式：这是绝大多数 OpenAI 兼容接入点的事实。
		SupportsTemperature: true,
		SupportsJSONMode:    true,
		Source:              model.CapabilitySourceBuiltin,
	}

	switch {
	case hasAnyPrefix(normalized, "gpt-5", "gpt-6", "o1", "o3", "o4"),
		hasAnyPrefix(normalized, "o1-", "o3-", "o4-"):
		// 推理型模型家族：拒绝 temperature，支持 reasoning_effort 与严格结构化输出。
		caps.SupportsTemperature = false
		caps.SupportsReasoningEffort = true
		caps.SupportsStructuredOutput = true
	case strings.Contains(normalized, "reasoner"),
		strings.Contains(normalized, "thinking"),
		strings.Contains(normalized, "reasoning"),
		strings.HasPrefix(normalized, "r1"),
		strings.HasPrefix(normalized, "deepseek-r"):
		// 带思考能力的开源/第三方模型：同样拒绝 temperature；是否支持
		// 严格结构化输出**未知**，因此保守声明不支持 —— 让调用方改用
		// 提示词约束 JSON，两者失败模式不同，不能混为一谈。
		caps.SupportsTemperature = false
		caps.SupportsReasoningEffort = true
	case strings.Contains(normalized, "claude"):
		// Anthropic 兼容网关：支持 temperature，但 OpenAI 风格的
		// response_format 通常不被透传，因此声明仅支持 JSON 模式。
		caps.SupportsStructuredOutput = false
	case providerType == "mock":
		// mock 只用于默认 CI 的可控假 provider；能力声明取最宽松形态即可，
		// 因为它不会真的发出请求（见各生成器的 mock 拒绝分支）。
		caps.SupportsStructuredOutput = true
	}

	return caps
}

// hasAnyPrefix 判断字符串是否以任一前缀开头。
func hasAnyPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// ValidateModelRequest 在提交外部请求前校验能力与请求是否一致。
//
// 返回 model.FieldErrors（中文字段级错误），与项目/文档校验同一形态，
// 使 API 层能用同一套 422 渲染。
func ValidateModelRequest(caps model.ModelCapabilities, spec model.ModelRequestSpec) model.FieldErrors {
	return caps.ValidateAgainstCapabilities(spec)
}

// RequestConfigFingerprint 生成请求配置的指纹（§2.4「config 指纹」）。
//
// 进账目的 config_fingerprint 必须能区分「同一连接同一模型下不同的参数组合」——
// 因为不同参数的成本不同（输出上限、思考强度都直接影响 token 数）。
// 它**不含**任何凭证与正文：只含影响计费与结果的参数。
//
// 温度用 strconv 的定长格式而不是 %v：浮点默认格式在不同 Go 版本/平台上
// 可能不同，那会让同一个配置产生两个指纹，进而让「同配置的成本对比」失效。
func RequestConfigFingerprint(spec model.ModelRequestSpec) string {
	temperature := ""
	if spec.Temperature != nil {
		// 'f' + 定点 6 位：消除 0.1 与 0.10000000000000001 这类表示差异。
		temperature = strconv.FormatFloat(*spec.Temperature, 'f', 6, 64)
	}
	canonical := struct {
		Endpoint         string `json:"endpoint"`
		Model            string `json:"model"`
		Temperature      string `json:"temperature"`
		MaxOutputTokens  int    `json:"maxOutputTokens"`
		ReasoningEffort  string `json:"reasoningEffort"`
		StructuredOutput bool   `json:"structuredOutput"`
		JSONMode         bool   `json:"jsonMode"`
		SchemaVersion    string `json:"schemaVersion"`
	}{
		Endpoint:         model.EndpointFingerprint(spec.EndpointURL),
		Model:            strings.ToLower(strings.TrimSpace(spec.ModelName)),
		Temperature:      temperature,
		MaxOutputTokens:  spec.MaxOutputTokens,
		ReasoningEffort:  strings.ToLower(strings.TrimSpace(spec.ReasoningEffort)),
		StructuredOutput: spec.StructuredOutput,
		JSONMode:         spec.JSONMode,
		SchemaVersion:    strings.TrimSpace(spec.SchemaVersion),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		// 这里的输入全是本地构造的基础类型，Marshal 不可能失败；
		// 若真的失败，返回一个**显式**标记而不是空串 —— 空串会被下游
		// 当成「没有配置指纹」，从而让不同配置的账目混在一起。
		return "unfingerprintable:" + fmt.Sprint(spec.ModelName)
	}
	sum := sha256.Sum256(raw)
	return "cfg:" + hex.EncodeToString(sum[:])[:32]
}
