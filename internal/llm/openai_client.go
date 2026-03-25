package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	if _, exists := payload["stream"]; !exists {
		payload["stream"] = false
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return decoded, err
	}

	url := strings.TrimRight(provider.BaseURL, "/") + "/chat/completions"
	client := &http.Client{Timeout: timeout}
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return decoded, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+provider.APIKey)

		res, err := client.Do(req)
		if err != nil {
			lastErr = err
			if shouldRetryTransportError(err) && attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return decoded, err
		}

		if res.StatusCode >= 300 {
			message, retryable := readProviderError(res)
			_ = res.Body.Close()
			lastErr = fmt.Errorf("provider request failed: %s", message)
			if retryable && attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return decoded, lastErr
		}

		raw, readErr := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return decoded, readErr
		}

		err = decodeChatCompletionBody(raw, &decoded)
		if err != nil {
			lastErr = err
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return decoded, err
		}
		if len(decoded.Choices) == 0 {
			lastErr = fmt.Errorf("provider returned no choices")
			if attempt < 3 {
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
		return nil
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
	if len(preview) > 220 {
		preview = preview[:220]
	}
	return fmt.Errorf("provider returned undecodable response: %s", preview)
}

func decodeChatCompletionSSE(raw []byte) (string, error) {
	lines := strings.Split(string(raw), "\n")
	var builder strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var chunk chatCompletionStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return "", err
		}

		wrote := false
		for _, choice := range chunk.Choices {
			text := firstNonEmpty(
				choice.Message.Content,
				choice.Message.ReasoningContent,
				choice.Delta.ReasoningContent,
				choice.Message.Text,
				choice.Delta.Content,
				choice.Delta.Text,
			)
			if text != "" {
				builder.WriteString(text)
				wrote = true
			}
		}

		if wrote {
			continue
		}

		var generic map[string]any
		if err := json.Unmarshal([]byte(payload), &generic); err != nil {
			return "", err
		}
		for _, text := range extractKnownText(generic) {
			builder.WriteString(text)
		}
	}

	if builder.Len() == 0 {
		return "", fmt.Errorf("no sse content")
	}
	return builder.String(), nil
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
			case "content", "reasoning_content", "reasoning", "text", "output_text":
				if text, ok := nested.(string); ok && strings.TrimSpace(text) != "" {
					collected = append(collected, text)
				}
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
