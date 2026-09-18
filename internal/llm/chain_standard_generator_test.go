package llm

import (
	"testing"

	"github.com/1420970597/llm/internal/model"
)

func TestParseChainStepsAcceptsObjectShape(t *testing.T) {
	content := `{"steps":[{"index":1,"title":"澄清目标","description":"确认任务边界","checkpoint":"目标可一句话复述"}]}`
	steps, err := parseChainSteps(content)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(steps) != 1 || steps[0].Title != "澄清目标" {
		t.Fatalf("unexpected steps: %+v", steps)
	}
}

func TestParseChainStepsAcceptsBareArray(t *testing.T) {
	content := `[{"index":1,"title":"识别约束"},{"index":2,"title":"推演方案"}]`
	steps, err := parseChainSteps(content)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(steps) != 2 || steps[1].Title != "推演方案" {
		t.Fatalf("unexpected steps: %+v", steps)
	}
}

func TestParseChainStepsAcceptsStringArray(t *testing.T) {
	steps, err := parseChainSteps(`["第一步","第二步"]`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(steps) != 2 || steps[0].Title != "第一步" {
		t.Fatalf("unexpected steps: %+v", steps)
	}
}

func TestParseChainStepsRejectsUnusableContent(t *testing.T) {
	if _, err := parseChainSteps("抱歉，我无法完成这个请求。"); err == nil {
		t.Fatal("expected error for non-JSON content")
	}
}

func TestNormalizeChainStepsReindexesAndDropsEmptyTitles(t *testing.T) {
	steps, err := normalizeChainSteps([]model.ChainStep{
		{Index: 99, Title: "  第一步  ", Description: " a "},
		{Index: 100, Title: "   "},
		{Index: 101, Title: "第二步"},
	}, 0)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("empty-title step must be dropped, got %d steps", len(steps))
	}
	if steps[0].Index != 1 || steps[1].Index != 2 {
		t.Fatalf("index must be renumbered from 1: %+v", steps)
	}
	if steps[0].Title != "第一步" {
		t.Fatalf("title must be trimmed, got %q", steps[0].Title)
	}
}

func TestNormalizeChainStepsTruncatesToLimit(t *testing.T) {
	steps, err := normalizeChainSteps([]model.ChainStep{
		{Title: "a"}, {Title: "b"}, {Title: "c"},
	}, 2)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
}

func TestNormalizeChainStepsErrorsWhenAllTitlesEmpty(t *testing.T) {
	if _, err := normalizeChainSteps([]model.ChainStep{{Title: "  "}, {Title: ""}}, 0); err == nil {
		t.Fatal("expected error when every title is empty")
	}
}

func TestBuildChainStandardPromptCarriesDirectionAndCount(t *testing.T) {
	prompt := buildChainStandardPrompt("军事", "海上巡逻", 6)
	if prompt == "" {
		t.Fatal("prompt must not be empty")
	}
	for _, want := range []string{"军事", "海上巡逻", "6", "checkpoint", "JSON"} {
		if !contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestMarshalChainStepsEncodesEmptySliceAsArray(t *testing.T) {
	payload, err := MarshalChainSteps(nil)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(payload) != "[]" {
		t.Fatalf("nil steps must encode as [], got %s", payload)
	}
}
