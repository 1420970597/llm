package main

import (
	"testing"

	"github.com/1420970597/llm/internal/cleaning"
	"github.com/1420970597/llm/internal/model"
)

// 这组测试锁定 L11 → L12 的接线缝。
//
// 背景：L12 落地时 L11 还没合并，job_cleaning.go 里是自建的临时匹配器，
// 只做 strings.Contains / HasPrefix / regexp，**没有归一化**。
// 接线到 cleaning.MatchKeywords 后，全角/半角与大小写应当等价。
// 若哪天有人把适配器改回自建实现，下面 TestKeywordMatcherNormalizesFullWidth
// 会立刻失败。
func testKeywords() []model.CleaningKeyword {
	return []model.CleaningKeyword{
		{ID: 1, IsActive: true, Pattern: "我不能", Category: cleaning.CategoryRefusal, MatchMode: "contains", Severity: cleaning.SeverityBlock},
		{ID: 2, IsActive: true, Pattern: "i cannot", Category: cleaning.CategoryEnglishRefusal, MatchMode: "contains", Severity: cleaning.SeverityBlock},
		{ID: 3, IsActive: true, Pattern: "对不起", Category: cleaning.CategoryRefusal, MatchMode: "prefix", Severity: cleaning.SeverityWarn},
	}
}

func TestKeywordMatcherDelegatesToL11(t *testing.T) {
	matcher := newKeywordMatcher(testKeywords())

	hits := matcher.Match("对不起，我不能回答这个问题。")
	if len(hits) != 2 {
		t.Fatalf("期望命中 2 条（prefix 的「对不起」与 contains 的「我不能」），实际 %d：%+v", len(hits), hits)
	}

	byPattern := map[string]cleaning.ScannerMatch{}
	for _, hit := range hits {
		byPattern[hit.Pattern] = hit
	}

	prefixHit, ok := byPattern["对不起"]
	if !ok {
		t.Fatal("缺少 prefix 模式命中")
	}
	if prefixHit.Severity != cleaning.SeverityWarn {
		t.Errorf("prefix 命中 severity 应为 warn，实际 %q", prefixHit.Severity)
	}
	if prefixHit.Category != cleaning.CategoryRefusal {
		t.Errorf("prefix 命中 category 应为 refusal，实际 %q", prefixHit.Category)
	}
	if prefixHit.Snippet == "" {
		t.Error("命中必须带 snippet，否则清洗报告无法定位")
	}

	containsHit, ok := byPattern["我不能"]
	if !ok {
		t.Fatal("缺少 contains 模式命中")
	}
	if containsHit.KeywordID != 1 {
		t.Errorf("contains 命中 KeywordID 应为 1，实际 %d", containsHit.KeywordID)
	}
}

// 这是接线的核心价值：自建实现做不到的全角/半角与大小写归一化。
//
// 注意断言方向：归一化让「同一个字符的不同宽度写法」等价，
// 所以测试要用「关键词是全角、正文是半角」（或反之）以及大小写差异，
// 而不能用「中间插了标点」——那本来就该不命中。
func TestKeywordMatcherNormalizesFullWidth(t *testing.T) {
	// 全角模式的「我不能，」（全角逗号）应当能命中半角逗号的正文。
	fullWidth := newKeywordMatcher([]model.CleaningKeyword{
		{ID: 10, IsActive: true, Pattern: "我不能，", Category: cleaning.CategoryRefusal, MatchMode: "contains", Severity: cleaning.SeverityBlock},
	})
	if hits := fullWidth.Match("对不起，我不能,回答这个问题"); len(hits) == 0 {
		t.Error("全角逗号关键词应命中半角逗号正文（全角/半角归一化失效）")
	}

	cases := []struct {
		name    string
		content string
	}{
		{"英文关键词命中全大写正文", "I CANNOT ANSWER THIS"},
		{"英文关键词命中混合大小写", "I Cannot Answer This"},
	}
	matcher := newKeywordMatcher(testKeywords())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if hits := matcher.Match(tc.content); len(hits) == 0 {
				t.Errorf("大小写折叠后应命中，但实际 0 条：content=%q", tc.content)
			}
		})
	}
}

// 反向锁定：关键词跨标点时不应当误命中。
// 这条与上一条成对，防止有人把「归一化」误实现成「删掉所有标点」。
func TestKeywordMatcherDoesNotIgnorePunctuation(t *testing.T) {
	matcher := newKeywordMatcher(testKeywords())
	if hits := matcher.Match("我，不能回答"); len(hits) != 0 {
		t.Errorf("「我不能」不应跨越逗号命中，实际 %d 条：%+v", len(hits), hits)
	}
}

func TestKeywordMatcherEmptyInputs(t *testing.T) {
	matcher := newKeywordMatcher(testKeywords())
	if hits := matcher.Match(""); len(hits) != 0 {
		t.Errorf("空内容不应命中，实际 %d 条", len(hits))
	}
	if hits := newKeywordMatcher(nil).Match("我不能回答"); len(hits) != 0 {
		t.Errorf("无关键词时不应命中，实际 %d 条", len(hits))
	}
}

// Match 是 Scanner 依赖的接口方法，签名必须稳定。
func TestKeywordMatcherSatisfiesMatcherInterface(t *testing.T) {
	var _ cleaning.Matcher = newKeywordMatcher(testKeywords())
}
