package llm

import (
	"context"
	"fmt"
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

// CompleteStructured 调用裁判模型并把正文解析进 target。
//
// 评估打分要求模型返回 JSON，而 unmarshalStructuredContent 是包内函数，
// 因此这里对外暴露一个结构化入口，供 internal/eval 直接使用。
func CompleteStructured(ctx context.Context, provider ProviderConfig, systemPrompt, userPrompt string, target any, timeout time.Duration) error {
	content, err := CompleteJSON(ctx, provider, systemPrompt, userPrompt, timeout)
	if err != nil {
		return err
	}
	return unmarshalStructuredContent(content, target)
}
