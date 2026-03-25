package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func unmarshalStructuredContent(raw string, target any) error {
	normalized := strings.TrimSpace(raw)
	if strings.HasPrefix(normalized, "```") {
		lines := strings.Split(normalized, "\n")
		if len(lines) >= 3 {
			normalized = strings.Join(lines[1:len(lines)-1], "\n")
		}
		normalized = strings.TrimSpace(normalized)
	}

	if err := json.Unmarshal([]byte(normalized), target); err == nil {
		return nil
	}

	if candidate := extractLikelyJSONBlock(normalized); candidate != "" {
		if err := json.Unmarshal([]byte(candidate), target); err == nil {
			return nil
		}
	}

	preview := truncateRunes(normalized, 220)
	return fmt.Errorf("provider returned non-JSON content: %s", preview)
}

func extractLikelyJSONBlock(input string) string {
	bytesInput := []byte(input)
	start := -1
	depth := 0
	inString := false
	escaped := false

	for i := 0; i < len(bytesInput); i++ {
		ch := bytesInput[i]
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
		case '{', '[':
			if depth == 0 {
				start = i
			}
			depth++
		case '}', ']':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				return strings.TrimSpace(string(bytesInput[start : i+1]))
			}
		}
	}
	return ""
}

func truncateRunes(input string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(input) <= maxRunes {
		return input
	}
	runes := []rune(input)
	return string(runes[:maxRunes])
}
