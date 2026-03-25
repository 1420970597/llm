package llm

import (
	"encoding/json"
	"fmt"
	"strings"
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

	start := strings.IndexAny(normalized, "[{")
	endArray := strings.LastIndex(normalized, "]")
	endObject := strings.LastIndex(normalized, "}")
	end := endArray
	if endObject > end {
		end = endObject
	}
	if start >= 0 && end > start {
		candidate := strings.TrimSpace(normalized[start : end+1])
		if err := json.Unmarshal([]byte(candidate), target); err == nil {
			return nil
		}
	}

	preview := normalized
	if len(preview) > 220 {
		preview = preview[:220]
	}
	return fmt.Errorf("provider returned non-JSON content: %s", preview)
}
