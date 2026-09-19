package llm

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// CompleteJSON 调用一个 OpenAI 兼容模型并返回正文文本。
//
// 供评估模块（internal/eval）作为「裁判模型」调用入口使用。
// 与各生成器保持一致：复用 requestChatCompletion（含重试与流式 SSE 解析）
// 与 applyReasoningEffort，避免各处重复实现 HTTP 细节。
//
// 注意：推理型模型的思考过程在 reasoning_content 中，已在 SSE 解析层与正文分离，
// 因此这里拿到的 content 是干净正文。
func CompleteJSON(ctx context.Context, provider ProviderConfig, systemPrompt, userPrompt string, timeout time.Duration) (string, error) {
	if provider.ProviderType == "mock" {
		return "", fmt.Errorf("mock provider is disabled in real-data mode")
	}
	if strings.TrimSpace(provider.BaseURL) == "" || strings.TrimSpace(provider.APIKey) == "" {
		return "", fmt.Errorf("judge provider configuration is incomplete")
	}
	if timeout <= 0 {
		timeout = 300 * time.Second
	}

	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, map[string]string{"role": "system", "content": systemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": userPrompt})

	payload := map[string]any{
		"model":    provider.Model,
		"messages": messages,
		"stream":   true,
	}
	applyReasoningEffort(payload, provider)

	decoded, err := requestChatCompletion(ctx, provider, payload, timeout)
	if err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("judge provider returned no choices")
	}

	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("judge provider returned empty content")
	}
	return content, nil
}

// judgeJSONAttempts 是结构化裁判调用的最大尝试次数。
//
// 为什么需要重试（父代理用真实端到端验收发现的缺陷）：
// 推理型裁判模型有时会把**思考过程**当成正文返回（实测 48 次调用里 6 次如此，
// 12.5%），内容形如「我们需要评估的维度是"表达精炼度"…」而不是 JSON。
// 而 CompleteStructured 此前**只尝试一次**，解析失败即把该条记为 failed。
//
// 后果不是「偶发失败可忽略」，而是**评分覆盖率下降**：那次验收 48 条里有 6 条
// 没有分数，整个运行被判为 partial_failed，报告的结论退化为
// 「此时不给出质量结论 —— 部分评分不足以代表整个数据集」。
//
// 3 次是刻意取的保守值：实测单次成功率约 87.5%，3 次独立尝试后仍全失败的概率
// 约 0.2%；同时不至于在模型**结构性**不支持 JSON 时无限重试。
const judgeJSONAttempts = 3

// CompleteStructured 调用裁判模型并把正文解析进 target。
//
// 评估打分要求模型返回 JSON，而 unmarshalStructuredContent 是包内函数，
// 因此这里对外暴露一个结构化入口，供 internal/eval 直接使用。
//
// 解析失败时**重试**，且重试时追加一条纠正指令 —— 只把同样的提示词再发一次
// 往往得到同样的跑偏结果，明确告诉模型「上次不是 JSON」更有效。
func CompleteStructured(ctx context.Context, provider ProviderConfig, systemPrompt, userPrompt string, target any, timeout time.Duration) error {
	var lastErr error
	attemptPrompt := userPrompt
	for attempt := 1; attempt <= judgeJSONAttempts; attempt++ {
		content, err := CompleteJSON(ctx, provider, systemPrompt, attemptPrompt, timeout)
		if err != nil {
			// 传输层失败：requestChatCompletion 内部已有自己的重试与退避，
			// 这里不再叠加，直接返回（避免两层重试相乘导致调用时间失控）。
			return err
		}
		if err := unmarshalStructuredContent(content, target); err == nil {
			return nil
		} else {
			lastErr = err
			log.Printf("judge.structured.parse_failed attempt=%d/%d model=%s err=%v",
				attempt, judgeJSONAttempts, provider.Model, err)
		}
		if attempt < judgeJSONAttempts {
			// 纠正指令：明确指出上次的问题与要求，而不是重复同一提示词。
			attemptPrompt = userPrompt +
				"\n\n【重要】你上一次的回复不是合法 JSON（可能是把思考过程当成了答案）。" +
				"这次请**只输出一个 JSON 对象**，不要任何解释、不要 Markdown 代码围栏、不要复述题目。"
		}
	}
	return fmt.Errorf("judge returned non-JSON content after %d attempts: %w", judgeJSONAttempts, lastErr)
}
