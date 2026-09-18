package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Text             string `json:"text"`
		} `json:"message"`
	} `json:"choices"`
}

type chatCompletionStreamChunk struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Text             string `json:"text"`
		} `json:"message"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Text             string `json:"text"`
		} `json:"delta"`
	} `json:"choices"`
}

func requestChatCompletion(ctx context.Context, provider ProviderConfig, payload map[string]any, timeout time.Duration) (chatCompletionResponse, error) {
	var decoded chatCompletionResponse
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	requestBodies, err := buildChatCompletionBodies(provider, payload)
	if err != nil {
		return decoded, err
	}

	url := strings.TrimRight(provider.BaseURL, "/") + "/chat/completions"
	client := &http.Client{Timeout: timeout}
	var lastErr error

	maxAttempts := len(requestBodies)
	if maxAttempts < 3 {
		maxAttempts = 3
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		requestBody := requestBodies[(attempt-1)%len(requestBodies)]
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestBody))
		if err != nil {
			return decoded, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)

		res, err := client.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("llm.chat.transport_error model=%s attempt=%d err=%v", provider.Model, attempt, err)
			if shouldRetryTransportError(err) && attempt < maxAttempts {
				select {
				case <-time.After(time.Duration(attempt) * time.Second):
				case <-ctx.Done():
					return decoded, ctx.Err()
				}
				continue
			}
			return decoded, err
		}

		if res.StatusCode >= 300 {
			message, retryable := readProviderError(res)
			_ = res.Body.Close()
			lastErr = fmt.Errorf("provider request failed: %s", message)
			if retryable && attempt < maxAttempts {
				select {
				case <-time.After(time.Duration(attempt) * time.Second):
				case <-ctx.Done():
					return decoded, ctx.Err()
				}
				continue
			}
			return decoded, lastErr
		}

		raw, readErr := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return decoded, readErr
		}

		err = decodeChatCompletionBody(raw, &decoded)
		if err != nil {
			lastErr = err
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return decoded, fmt.Errorf("provider response decode failed after %d attempts: %w", attempt, err)
		}
		if len(decoded.Choices) == 0 {
			lastErr = fmt.Errorf("provider returned no choices")
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return decoded, lastErr
		}
		return decoded, nil
	}

	if lastErr != nil {
		return decoded, lastErr
	}
	return decoded, fmt.Errorf("provider request failed")
}

func buildChatCompletionBodies(provider ProviderConfig, payload map[string]any) ([][]byte, error) {
	base := clonePayload(payload)
	if _, exists := base["stream"]; !exists {
		base["stream"] = true
	}

	if strings.HasPrefix(strings.ToLower(provider.Model), "gpt-5") {
		if _, hasReasoning := base["reasoning_effort"]; hasReasoning {
			delete(base, "temperature")
		}
		if _, hasMaxTokens := base["max_tokens"]; !hasMaxTokens {
			if _, hasMaxCompletionTokens := base["max_completion_tokens"]; !hasMaxCompletionTokens {
				base["max_completion_tokens"] = 4096
			}
		}
	}

	variants := []map[string]any{base}
	if strings.HasPrefix(strings.ToLower(provider.Model), "gpt-5") {
		altNonStream := clonePayload(base)
		altNonStream["stream"] = false
		variants = append(variants, altNonStream)

		altTokens := clonePayload(base)
		if _, hasMaxTokens := altTokens["max_tokens"]; !hasMaxTokens {
			if value, ok := altTokens["max_completion_tokens"]; ok {
				altTokens["max_tokens"] = value
				delete(altTokens, "max_completion_tokens")
			} else {
				altTokens["max_tokens"] = 4096
			}
			variants = append(variants, altTokens)
		}

		if _, hasReasoning := base["reasoning_effort"]; hasReasoning {
			altNoReasoning := clonePayload(base)
			delete(altNoReasoning, "reasoning_effort")
			variants = append(variants, altNoReasoning)

			altNoReasoningNoTemp := clonePayload(altNoReasoning)
			delete(altNoReasoningNoTemp, "temperature")
			variants = append(variants, altNoReasoningNoTemp)
		}
	}

	bodies := make([][]byte, 0, len(variants))
	seen := map[string]struct{}{}
	for _, variant := range variants {
		body, err := json.Marshal(variant)
		if err != nil {
			return nil, err
		}
		signature := string(body)
		if _, exists := seen[signature]; exists {
			continue
		}
		seen[signature] = struct{}{}
		bodies = append(bodies, body)
	}
	if len(bodies) == 0 {
		return nil, fmt.Errorf("provider payload empty")
	}
	return bodies, nil
}

func clonePayload(payload map[string]any) map[string]any {
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func shouldRetryTransportError(err error) bool {
	if err == nil {
		return false
	}
	if netErr, ok := err.(net.Error); ok && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "eof") ||
		strings.Contains(message, "timeout")
}

func readProviderError(res *http.Response) (string, bool) {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = res.Status
	} else {
		message = res.Status + " " + message
	}
	retryable := res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusBadGateway || res.StatusCode == http.StatusServiceUnavailable || res.StatusCode == http.StatusGatewayTimeout
	return message, retryable
}

func decodeChatCompletionBody(raw []byte, target *chatCompletionResponse) error {
	trimmed := bytes.TrimSpace(raw)
	if err := json.Unmarshal(trimmed, target); err == nil {
		if len(target.Choices) > 0 && target.Choices[0].Message.Content == "" {
			target.Choices[0].Message.Content = firstNonEmpty(
				target.Choices[0].Message.ReasoningContent,
				target.Choices[0].Message.Text,
			)
		}
		if len(target.Choices) > 0 && strings.TrimSpace(target.Choices[0].Message.Content) != "" {
			return nil
		}
	}

	var generic map[string]any
	if err := json.Unmarshal(trimmed, &generic); err == nil {
		text := strings.Join(extractKnownText(generic), "")
		if strings.TrimSpace(text) != "" {
			target.Choices = make([]struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Text             string `json:"text"`
				} `json:"message"`
			}, 1)
			target.Choices[0].Message.Content = text
			return nil
		}
	}

	content, err := decodeChatCompletionSSE(trimmed)
	if err == nil && content != "" {
		target.Choices = make([]struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Text             string `json:"text"`
			} `json:"message"`
		}, 1)
		target.Choices[0].Message.Content = content
		return nil
	}

	preview := string(trimmed)
	return fmt.Errorf("provider returned undecodable response (len=%d): %s", len(trimmed), preview)
}

func decodeChatCompletionSSE(raw []byte) (string, error) {
	lines := strings.Split(string(raw), "\n")
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	eventPayload := make([]string, 0, 4)
	sawNonContentEvent := false

	flushEvent := func() {
		if len(eventPayload) == 0 {
			return
		}
		payload := strings.TrimSpace(strings.Join(eventPayload, ""))
		eventPayload = eventPayload[:0]
		if payload == "" || payload == "[DONE]" {
			return
		}
		if content, reasoning, ok := extractSSEPayloadText(payload); ok && (content != "" || reasoning != "") {
			contentBuilder.WriteString(content)
			reasoningBuilder.WriteString(reasoning)
			return
		}
		if isSSEMetadataOnlyPayload(payload) {
			sawNonContentEvent = true
		}
	}

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flushEvent()
			continue
		}
		if !strings.HasPrefix(trimmed, "data:") {
			if len(eventPayload) > 0 {
				eventPayload[len(eventPayload)-1] += strings.TrimSpace(line)
			}
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		if content, reasoning, ok := extractSSEPayloadText(payload); ok {
			flushEvent()
			if content != "" || reasoning != "" {
				contentBuilder.WriteString(content)
				reasoningBuilder.WriteString(reasoning)
			} else if isSSEMetadataOnlyPayload(payload) {
				sawNonContentEvent = true
			}
			continue
		}
		if isSSEMetadataOnlyPayload(payload) {
			sawNonContentEvent = true
			continue
		}

		eventPayload = append(eventPayload, payload)
	}
	flushEvent()

	// 正文优先：推理型模型会先流式输出 reasoning_content，再输出 content。
	// 二者必须分开累计，否则思考过程会被当成正文返回。
	if strings.TrimSpace(contentBuilder.String()) != "" {
		return contentBuilder.String(), nil
	}
	if strings.TrimSpace(reasoningBuilder.String()) != "" {
		return reasoningBuilder.String(), nil
	}

	if fallback, sawMetadata := extractEmbeddedSSEContent(string(raw)); strings.TrimSpace(fallback) != "" {
		return fallback, nil
	} else if sawMetadata {
		sawNonContentEvent = true
	}
	if sawNonContentEvent {
		return "", fmt.Errorf("sse stream contained metadata chunks but no content")
	}
	return "", fmt.Errorf("no sse content")
}

func extractEmbeddedSSEContent(raw string) (string, bool) {
	payloads := extractJSONObjectPayloads(raw)
	if len(payloads) == 0 {
		return "", false
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	sawMetadata := false
	for _, payload := range payloads {
		content, reasoning, ok := extractSSEPayloadText(payload)
		if !ok {
			continue
		}
		contentBuilder.WriteString(content)
		reasoningBuilder.WriteString(reasoning)
		if content != "" || reasoning != "" {
			continue
		}
		if isSSEMetadataOnlyPayload(payload) {
			sawMetadata = true
		}
	}
	if strings.TrimSpace(contentBuilder.String()) != "" {
		return contentBuilder.String(), sawMetadata
	}
	return reasoningBuilder.String(), sawMetadata
}

func extractJSONObjectPayloads(raw string) []string {
	bytesRaw := []byte(raw)
	payloads := make([]string, 0, 8)
	depth := 0
	start := -1
	inString := false
	escaped := false

	for i := 0; i < len(bytesRaw); i++ {
		ch := bytesRaw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				candidate := strings.TrimSpace(string(bytesRaw[start : i+1]))
				if strings.Contains(candidate, `"choices"`) {
					payloads = append(payloads, candidate)
				}
				start = -1
			}
		}
	}

	return payloads
}

// extractSSEPayloadText 分别返回正文与思考过程。
//
// 推理型模型（如 deepseek 系）会在流式响应中先发 reasoning_content 增量，
// 再发 content 增量。两者必须分开累计，否则思考过程会被误当成正文，
// 导致下游 JSON 解析失败。
func extractSSEPayloadText(payload string) (content string, reasoning string, ok bool) {
	var chunk chatCompletionStreamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
		var contentBuilder strings.Builder
		var reasoningBuilder strings.Builder
		for _, choice := range chunk.Choices {
			contentBuilder.WriteString(firstNonEmpty(
				choice.Message.Content,
				choice.Message.Text,
				choice.Delta.Content,
				choice.Delta.Text,
			))
			reasoningBuilder.WriteString(firstNonEmpty(
				choice.Message.ReasoningContent,
				choice.Delta.ReasoningContent,
			))
		}
		if contentBuilder.Len() > 0 || reasoningBuilder.Len() > 0 {
			return contentBuilder.String(), reasoningBuilder.String(), true
		}
	}

	var generic map[string]any
	if err := json.Unmarshal([]byte(payload), &generic); err != nil {
		return "", "", false
	}
	return joinGenericText(generic, contentKeys), joinGenericText(generic, reasoningKeys), true
}

// 正文类字段与思考类字段必须分开处理，避免把思考过程混入正文。
var contentKeys = map[string]struct{}{
	"content": {}, "text": {}, "output_text": {},
}

var reasoningKeys = map[string]struct{}{
	"reasoning_content": {}, "reasoning": {},
}

func joinGenericText(value any, wanted map[string]struct{}) string {
	return strings.Join(extractTextByKeys(value, wanted), "")
}

func extractTextByKeys(value any, wanted map[string]struct{}) []string {
	switch typed := value.(type) {
	case map[string]any:
		collected := []string{}
		for key, nested := range typed {
			if _, match := wanted[key]; match {
				if text, ok := nested.(string); ok {
					if strings.TrimSpace(text) != "" {
						collected = append(collected, text)
					}
					continue
				}
			}
			collected = append(collected, extractTextByKeys(nested, wanted)...)
		}
		return collected
	case []any:
		collected := []string{}
		for _, nested := range typed {
			collected = append(collected, extractTextByKeys(nested, wanted)...)
		}
		return collected
	default:
		return nil
	}
}

func isSSEMetadataOnlyPayload(payload string) bool {
	normalized := strings.ToLower(payload)
	if strings.Contains(normalized, `"object":"chat.completion.chunk"`) &&
		(strings.Contains(normalized, `"choices":[]`) || strings.Contains(normalized, `"choices": []`)) &&
		!strings.Contains(normalized, `"content":"`) &&
		!strings.Contains(normalized, `"reasoning_content":"`) &&
		!strings.Contains(normalized, `"text":"`) {
		return true
	}

	var generic map[string]any
	if err := json.Unmarshal([]byte(payload), &generic); err != nil {
		return false
	}
	object, _ := generic["object"].(string)
	if !strings.Contains(object, "chat.completion.chunk") {
		return false
	}
	choices, ok := generic["choices"].([]any)
	if !ok {
		return false
	}
	if len(choices) == 0 {
		return true
	}
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			return false
		}
		if strings.TrimSpace(strings.Join(extractKnownText(choice), "")) != "" {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func extractKnownText(value any) []string {
	switch typed := value.(type) {
	case map[string]any:
		collected := []string{}
		for key, nested := range typed {
			switch key {
			case "content", "reasoning_content", "reasoning", "text", "output_text", "delta":
				if text, ok := nested.(string); ok && strings.TrimSpace(text) != "" {
					collected = append(collected, text)
					continue
				}
				collected = append(collected, extractKnownText(nested)...)
			default:
				collected = append(collected, extractKnownText(nested)...)
			}
		}
		return collected
	case []any:
		collected := []string{}
		for _, nested := range typed {
			collected = append(collected, extractKnownText(nested)...)
		}
		return collected
	default:
		return nil
	}
}
