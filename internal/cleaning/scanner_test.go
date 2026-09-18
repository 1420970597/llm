package cleaning

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// fakeMatcher 用子串包含模拟关键词命中，使扫描逻辑的测试不依赖 L11 的关键词库。
type fakeMatcher struct {
	patterns []string
}

func (m fakeMatcher) Match(content string) []ScannerMatch {
	matches := []ScannerMatch{}
	for index, pattern := range m.patterns {
		at := strings.Index(content, pattern)
		if at < 0 {
			continue
		}
		matches = append(matches, ScannerMatch{
			KeywordID:   int64(index + 1),
			Pattern:     pattern,
			Category:    "refusal",
			MatchedText: pattern,
			Snippet:     content[at : at+len(pattern)],
			Severity:    "block",
		})
	}
	return matches
}

func targets() []ScanTarget {
	return []ScanTarget{
		{QuestionID: 1, Stage: StageQuestion, Content: "海上巡逻需要注意什么"},
		{QuestionID: 1, Stage: StageReasoning, Content: "先确认海域气象，再规划航线"},
		{QuestionID: 2, Stage: StageAnswer, Content: "对不起，我不能帮你做这个"},
	}
}

func TestScanClassifiesEachStageSeparately(t *testing.T) {
	matcher := fakeMatcher{patterns: []string{"对不起", "我不能"}}
	results := Scan(targets(), matcher, nil)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, result := range results {
		switch result.Target.Stage {
		case StageQuestion, StageReasoning:
			if len(result.Matches) != 0 {
				t.Errorf("stage %s should have no match, got %d", result.Target.Stage, len(result.Matches))
			}
			if result.Action != ActionClean {
				t.Errorf("stage %s should be clean, got %s", result.Target.Stage, result.Action)
			}
		case StageAnswer:
			if len(result.Matches) != 2 {
				t.Errorf("answer stage should match 2 keywords, got %d", len(result.Matches))
			}
			if result.Action != ActionFlag {
				t.Errorf("answer stage without rules should default to flag, got %s", result.Action)
			}
		}
	}
}

func TestMinHitsBoundaryTriggersAtExactCount(t *testing.T) {
	matcher := fakeMatcher{patterns: []string{"对不起", "我不能"}}
	rules := []RuleSpec{{Name: "two-hits-drops", MinHits: 2, Action: ActionDrop, Priority: 10}}

	// 命中 2 次，恰好等于 min_hits —— 必须触发 drop。
	hit := Scan([]ScanTarget{{QuestionID: 2, Stage: StageAnswer, Content: "对不起，我不能帮你"}}, matcher, rules)
	if hit[0].Action != ActionDrop {
		t.Fatalf("2 hits with min_hits=2 should drop, got %s", hit[0].Action)
	}

	// 命中 1 次，低于 min_hits —— 不得触发 drop。
	below := Scan([]ScanTarget{{QuestionID: 3, Stage: StageAnswer, Content: "对不起，我做不到"}}, matcher, rules)
	if below[0].Action == ActionDrop {
		t.Fatalf("1 hit with min_hits=2 must not drop, got %s", below[0].Action)
	}
}

func TestRuleStageScopeExcludesOtherStages(t *testing.T) {
	matcher := fakeMatcher{patterns: []string{"对不起"}}
	answerOnly := []RuleSpec{{Name: "answer-only", StageScope: []string{StageAnswer}, MinHits: 1, Action: ActionDrop, Priority: 10}}

	results := Scan([]ScanTarget{
		{QuestionID: 1, Stage: StageReasoning, Content: "对不起"},
		{QuestionID: 1, Stage: StageAnswer, Content: "对不起"},
	}, matcher, answerOnly)

	if results[0].Action != ActionFlag {
		t.Errorf("reasoning stage is out of scope, expected flag, got %s", results[0].Action)
	}
	if results[1].Action != ActionDrop {
		t.Errorf("answer stage is in scope, expected drop, got %s", results[1].Action)
	}
}

func TestHigherPriorityRuleWins(t *testing.T) {
	matcher := fakeMatcher{patterns: []string{"对不起"}}
	rules := []RuleSpec{
		{Name: "low", MinHits: 1, Action: ActionFlag, Priority: 1},
		{Name: "high", MinHits: 1, Action: ActionDrop, Priority: 99},
	}
	results := Scan([]ScanTarget{{QuestionID: 1, Stage: StageAnswer, Content: "对不起"}}, matcher, rules)
	if results[0].Action != ActionDrop {
		t.Fatalf("higher priority rule should win, got %s", results[0].Action)
	}
}

func TestNormalizeStagesRejectsUnknownStage(t *testing.T) {
	if _, err := NormalizeStages([]string{"question", "answer_typo"}); err == nil {
		t.Fatal("unknown stage must be rejected instead of silently dropped")
	}
	got, err := NormalizeStages(nil)
	if err != nil {
		t.Fatalf("nil stages should default, got %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 default stages, got %v", got)
	}
}

func TestBuildReportHitRateAndZeroScanDoesNotPanic(t *testing.T) {
	run := model.CleaningRun{ID: 7, DatasetID: 1, Stages: DefaultStages()}
	results := []ScanResult{
		{Target: ScanTarget{QuestionID: 1, Stage: StageQuestion, Content: "正常问题"}},
		{Target: ScanTarget{QuestionID: 2, Stage: StageQuestion, Content: "对不起"}, Matches: []ScannerMatch{{KeywordID: 1, Pattern: "对不起", Category: "refusal", Snippet: "对不起"}}, Action: ActionFlag},
		{Target: ScanTarget{QuestionID: 2, Stage: StageAnswer, Content: "对不起"}, Matches: []ScannerMatch{{KeywordID: 1, Pattern: "对不起", Category: "refusal", Snippet: "对不起"}}, Action: ActionDrop},
	}

	report := BuildReport(run, results)

	byStage := map[string]model.CleaningStageStat{}
	for _, stat := range report.Stages {
		byStage[stat.Stage] = stat
	}
	if got := byStage[StageQuestion]; got.ScannedItems != 2 || got.FlaggedItems != 1 || got.HitRate != 0.5 {
		t.Errorf("question stage stat wrong: %+v", got)
	}
	// 该阶段一条都没扫到，HitRate 必须是 0 而不是 NaN/panic。
	if got := byStage[StageReasoning]; got.ScannedItems != 0 || got.HitRate != 0 {
		t.Errorf("empty stage must keep HitRate 0: %+v", got)
	}
	if got := byStage[StageAnswer]; got.DroppedItems != 1 {
		t.Errorf("answer stage should record 1 dropped, got %+v", got)
	}
	if len(report.Conclusions) == 0 {
		t.Error("report must contain conclusions")
	}
	// 同一个关键词在两个阶段各命中一次，聚合后 Hits 应为 2。
	if len(report.TopKeywords) != 1 || report.TopKeywords[0].Hits != 2 {
		t.Errorf("top keywords should aggregate hits across stages, got %+v", report.TopKeywords)
	}
}

func TestCleaningStatusUpdatesPrefersDrop(t *testing.T) {
	results := []ScanResult{
		{Target: ScanTarget{QuestionID: 5, Stage: StageQuestion}, Matches: []ScannerMatch{{KeywordID: 1}}, Action: ActionFlag},
		{Target: ScanTarget{QuestionID: 5, Stage: StageAnswer}, Matches: []ScannerMatch{{KeywordID: 2}}, Action: ActionDrop},
		{Target: ScanTarget{QuestionID: 6, Stage: StageQuestion}, Action: ActionClean},
	}
	updates := CleaningStatusUpdates(results)
	if updates[5] != ActionDrop {
		t.Errorf("question 5 should be dropped, got %q", updates[5])
	}
	if _, ok := updates[6]; ok {
		t.Error("clean question must not receive a status update")
	}
}

func TestFindingsFromResultsExpandsEachHit(t *testing.T) {
	results := []ScanResult{{
		Target: ScanTarget{QuestionID: 9, Stage: StageAnswer},
		Matches: []ScannerMatch{
			{KeywordID: 1, Pattern: "对不起", MatchedText: "对不起", Snippet: "对不起"},
			{KeywordID: 2, Pattern: "我不能", MatchedText: "我不能", Snippet: "我不能"},
		},
		Action: ActionDrop,
	}}
	findings := FindingsFromResults(11, 3, results)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	for _, finding := range findings {
		if finding.CleaningRunID != 11 || finding.DatasetID != 3 || finding.QuestionID != 9 || finding.Stage != StageAnswer || finding.Action != ActionDrop {
			t.Errorf("finding fields not propagated: %+v", finding)
		}
	}
}
