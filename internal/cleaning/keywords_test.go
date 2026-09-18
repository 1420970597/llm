package cleaning

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

func kw(id int64, pattern, mode, severity string) model.CleaningKeyword {
	return model.CleaningKeyword{
		ID: id, Pattern: pattern, Category: CategoryRefusal,
		MatchMode: mode, Severity: severity, IsBuiltin: true, IsActive: true,
	}
}

func TestBuiltinKeywordsCoverageAndUniqueness(t *testing.T) {
	all := BuiltinKeywords()
	if len(all) < 40 {
		t.Fatalf("内置关键词应至少 40 条，实际 %d", len(all))
	}

	seen := map[string]bool{}
	for _, k := range all {
		key := k.Category + "\x00" + k.Pattern
		if seen[key] {
			t.Errorf("pattern 在同一分类下重复: %s / %s", k.Category, k.Pattern)
		}
		seen[key] = true

		if k.Pattern == "" {
			t.Errorf("存在空 pattern")
		}
		if !k.IsBuiltin {
			t.Errorf("内置库出现 IsBuiltin=false: %s", k.Pattern)
		}
		if !k.IsActive {
			t.Errorf("内置库出现 IsActive=false: %s", k.Pattern)
		}
		switch k.MatchMode {
		case ModeContains, ModePrefix, ModeRegex:
		default:
			t.Errorf("未知 match_mode %q (pattern=%s)", k.MatchMode, k.Pattern)
		}
		switch k.Severity {
		case SeverityBlock, SeverityWarn:
		default:
			t.Errorf("未知 severity %q (pattern=%s)", k.Severity, k.Pattern)
		}
	}

	// 每个要求的分类都必须真实存在，防止以后有人删掉一整类。
	for _, category := range []string{
		CategoryRefusal, CategorySafety, CategoryUncertaintyEvasion,
		CategoryEnglishRefusal, CategoryPlaceholder,
	} {
		count := 0
		for _, k := range all {
			if k.Category == category {
				count++
			}
		}
		if count == 0 {
			t.Errorf("分类 %s 没有任何内置关键词", category)
		}
	}
}

// 需求原文点名的场景：「对不起，我不能帮你做这个」必须命中 refusal，且片段中文完好。
func TestMatchKeywordsChineseRefusalWithRuneSafeSnippet(t *testing.T) {
	content := "对不起，我不能帮你做这个"
	matches := MatchKeywords(content, BuiltinKeywords())

	if len(matches) == 0 {
		t.Fatalf("期望命中拒答关键词，实际 0 条")
	}

	var foundRefusal bool
	for _, m := range matches {
		if m.Category == CategoryRefusal {
			foundRefusal = true
		}
		// snippet 必须是合法 UTF-8 且是原文的子串——按 byte 切中文会破坏这一点。
		if !strings.Contains(content, m.Snippet) {
			t.Errorf("snippet 不是原文子串（可能按 byte 切坏了）: %q", m.Snippet)
		}
		if !utf8Valid(m.Snippet) {
			t.Errorf("snippet 不是合法 UTF-8: %q", m.Snippet)
		}
	}
	if !foundRefusal {
		t.Errorf("期望命中 refusal 类，实际命中 %+v", matches)
	}

	// 「对不起」与「我不能」都应各自命中一次。
	patterns := map[string]int{}
	for _, m := range matches {
		patterns[m.Pattern]++
	}
	if patterns["对不起"] != 1 || patterns["我不能"] != 1 {
		t.Errorf("期望「对不起」「我不能」各命中 1 次，实际 %v", patterns)
	}
}

// 防误伤：正常业务内容不得命中。
func TestMatchKeywordsNoFalsePositiveOnNormalContent(t *testing.T) {
	content := "海上巡逻需要先确认海域气象，再规划航线与补给点。"
	if matches := MatchKeywords(content, BuiltinKeywords()); len(matches) != 0 {
		t.Errorf("正常内容不应命中，实际 %+v", matches)
	}
}

// 「作为一个AI」是 prefix 模式，句中出现时不应命中。
func TestMatchKeywordsPrefixMode(t *testing.T) {
	keywords := BuiltinKeywords()

	atStart := MatchKeywords("作为一个AI，我建议你自行判断。", keywords)
	if !containsPattern(atStart, "作为一个AI") {
		t.Errorf("句首的「作为一个AI」应命中，实际 %+v", atStart)
	}

	inMiddle := MatchKeywords("本文介绍作为一个AI系统的基本原理。", keywords)
	if containsPattern(inMiddle, "作为一个AI") {
		t.Errorf("句中的「作为一个AI」不应命中（prefix 模式），实际 %+v", inMiddle)
	}
}

func TestMatchKeywordsRegexMode(t *testing.T) {
	// 直接取内置库里的 regex 关键词，验证**发布出去的**正则真能匹配。
	var regexKeywords []model.CleaningKeyword
	for _, k := range BuiltinKeywords() {
		if k.MatchMode == ModeRegex {
			regexKeywords = append(regexKeywords, k)
		}
	}
	if len(regexKeywords) == 0 {
		t.Fatalf("内置库应至少有一个 regex 关键词")
	}

	// 中文行文用的是 U+2026「…」，不是三个 ASCII 点号。
	hits := MatchKeywords("答案：这里省略了推导……然后直接给结论。", regexKeywords)
	if len(hits) != 1 {
		t.Fatalf("期望正则命中 1 条（U+2026 省略号），实际 %d", len(hits))
	}
	if !strings.Contains(hits[0].MatchedText, "…") {
		t.Errorf("MatchedText 应含省略号，实际 %q", hits[0].MatchedText)
	}
	if hits[0].Severity != SeverityWarn {
		t.Errorf("占位类应为 warn 以免误伤，实际 %q", hits[0].Severity)
	}

	// 三个 ASCII 点号同样应命中。
	if got := MatchKeywords("这里省略了推导...然后继续。", regexKeywords); len(got) != 1 {
		t.Errorf("三个 ASCII 点号应命中，实际 %+v", got)
	}

	// 普通句末句号与版本号不应命中。
	if got := MatchKeywords("版本 1.2 已发布。", regexKeywords); len(got) != 0 {
		t.Errorf("不应命中，实际 %+v", got)
	}

	// 非法正则不得阻断整轮扫描。
	broken := []model.CleaningKeyword{kw(9, `([unclosed`, ModeRegex, SeverityWarn)}
	if got := MatchKeywords("任意内容", broken); len(got) != 0 {
		t.Errorf("非法正则应被跳过，实际 %+v", got)
	}
}

func TestMatchKeywordsContainsModeAndNormalization(t *testing.T) {
	keywords := []model.CleaningKeyword{kw(1, "I cannot", ModeContains, SeverityBlock)}

	// 大小写归一化后应命中。
	if hits := MatchKeywords("Sorry, I CANNOT help with that.", keywords); len(hits) != 1 {
		t.Errorf("大小写不同应命中，实际 %d 条", len(hits))
	}
	// 全角字符归一化后应命中（全角 I）。
	if hits := MatchKeywords("ｓｏｒｒｙ, I cannot help.", keywords); len(hits) != 1 {
		t.Errorf("全角混排应命中，实际 %d 条", len(hits))
	}
	// MatchedText 保留原文大小写。
	hits := MatchKeywords("Sorry, I CANNOT help.", keywords)
	if len(hits) == 1 && hits[0].MatchedText != "I CANNOT" {
		t.Errorf("MatchedText 应保留原文大小写，实际 %q", hits[0].MatchedText)
	}
}

func TestMatchKeywordsEdgeCases(t *testing.T) {
	keywords := BuiltinKeywords()

	if got := MatchKeywords("", keywords); got == nil {
		t.Errorf("空输入应返回非 nil 空切片")
	} else if len(got) != 0 {
		t.Errorf("空输入不应命中，实际 %+v", got)
	}
	if got := MatchKeywords("   \n\t  ", keywords); len(got) != 0 {
		t.Errorf("纯空白不应命中，实际 %+v", got)
	}
	if got := MatchKeywords("对不起", nil); len(got) != 0 {
		t.Errorf("空关键词库不应命中，实际 %+v", got)
	}

	// 停用与空 pattern 必须跳过。
	inactive := []model.CleaningKeyword{
		{ID: 1, Pattern: "对不起", Category: CategoryRefusal, MatchMode: ModeContains, IsActive: false},
		{ID: 2, Pattern: "", Category: CategoryRefusal, MatchMode: ModeContains, IsActive: true},
	}
	if got := MatchKeywords("对不起", inactive); len(got) != 0 {
		t.Errorf("停用/空 pattern 不应命中，实际 %+v", got)
	}

	// 超长文本不得 panic，且命中数有界。
	long := strings.Repeat("对不起", 5000)
	if got := MatchKeywords(long, keywords); len(got) != 1 {
		t.Errorf("同一关键词多次命中应只返回 1 条，实际 %d", len(got))
	}
}

func TestSnippetRuneBoundaries(t *testing.T) {
	content := "前缀前缀对不起后缀后缀"
	// rune 下标：「对」=4，「不」=5，「起」=6，因此区间为 [4,7)。
	got := Snippet(content, 4, 7, 2)
	if got != "前缀对不起后缀" {
		t.Errorf("Snippet 结果不符，实际 %q", got)
	}

	// 越界与反向区间不得 panic。
	if got := Snippet(content, -5, 999, 30); got != content {
		t.Errorf("越界区间应被裁剪为全文，实际 %q", got)
	}
	if got := Snippet(content, 5, 2, 30); got == "" {
		t.Errorf("反向区间不应返回空串，实际 %q", got)
	}
	if got := Snippet("", 0, 5, 3); got != "" {
		t.Errorf("空内容应返回空串，实际 %q", got)
	}
}

func TestEvaluateRulesMinHitsBoundary(t *testing.T) {
	matches := []Match{{Pattern: "对不起", Category: CategoryRefusal}}

	// min_hits=2 而只命中 1 次 → 不触发该规则。
	rules := []model.CleaningRule{{
		Name: "strict", StageScope: []string{"answer"}, MinHits: 2,
		Action: "drop", Priority: 10, IsActive: true,
	}}
	action, hits := EvaluateRules(matches, rules, "answer")
	if hits != 1 {
		t.Fatalf("命中数应为 1，实际 %d", hits)
	}
	if action != "keep" {
		t.Errorf("min_hits=2 且命中 1 次应不触发，实际 action=%q", action)
	}

	// 命中数 == min_hits 时应触发（边界包含）。
	rules[0].MinHits = 1
	if action, _ := EvaluateRules(matches, rules, "answer"); action != "drop" {
		t.Errorf("命中数等于 min_hits 应触发 drop，实际 %q", action)
	}
}

func TestEvaluateRulesStageScopeAndPriority(t *testing.T) {
	matches := []Match{{Pattern: "对不起"}}

	rules := []model.CleaningRule{
		{Name: "answer-only", StageScope: []string{"answer"}, MinHits: 1, Action: "drop", Priority: 1, IsActive: true},
		{Name: "question", StageScope: []string{"question"}, MinHits: 1, Action: "flag", Priority: 2, IsActive: true},
	}

	// 阶段不匹配的规则不生效。
	if action, _ := EvaluateRules(matches, rules, "reasoning"); action != "keep" {
		t.Errorf("无适用规则应返回 keep，实际 %q", action)
	}
	// 优先级小者先判定。
	if action, _ := EvaluateRules(matches, rules, "answer"); action != "drop" {
		t.Errorf("priority=1 的规则应先命中，实际 %q", action)
	}

	// 空 StageScope 视为全阶段生效。
	global := []model.CleaningRule{
		{Name: "global", StageScope: nil, MinHits: 1, Action: "flag", Priority: 5, IsActive: true},
	}
	if action, _ := EvaluateRules(matches, global, "reasoning"); action != "flag" {
		t.Errorf("空 StageScope 应全阶段生效，实际 %q", action)
	}

	// 停用规则被跳过。
	disabled := []model.CleaningRule{
		{Name: "off", MinHits: 1, Action: "drop", Priority: 1, IsActive: false},
	}
	if action, _ := EvaluateRules(matches, disabled, "answer"); action != "keep" {
		t.Errorf("停用规则不应生效，实际 %q", action)
	}
}

func TestEvaluateRulesNoMatches(t *testing.T) {
	rules := []model.CleaningRule{
		{Name: "any", MinHits: 1, Action: "drop", Priority: 1, IsActive: true},
	}
	action, hits := EvaluateRules(nil, rules, "answer")
	if hits != 0 || action != "keep" {
		t.Errorf("零命中应返回 (keep, 0)，实际 (%q, %d)", action, hits)
	}
}

func containsPattern(matches []Match, pattern string) bool {
	for _, m := range matches {
		if m.Pattern == pattern {
			return true
		}
	}
	return false
}

func utf8Valid(s string) bool {
	return len([]rune(s)) > 0 && string([]rune(s)) == s
}
