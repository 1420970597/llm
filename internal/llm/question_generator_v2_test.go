package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 契约要求：x=10、mix={easy:0.3, medium:0.5, hard:0.2} 时得到 3/5/2。
func TestAllocateDifficultyMixMatchesContractExample(t *testing.T) {
	got := AllocateDifficultyMix(10, map[string]float64{
		DifficultyEasy:   0.3,
		DifficultyMedium: 0.5,
		DifficultyHard:   0.2,
	})
	want := map[string]int{DifficultyEasy: 3, DifficultyMedium: 5, DifficultyHard: 2}
	for level, count := range want {
		if got[level] != count {
			t.Errorf("allocation[%s] = %d, want %d (full: %v)", level, got[level], count, got)
		}
	}
}

// 最大余数法必须保证各档之和恰好等于 total，覆盖除不尽与极小数量。
func TestAllocateDifficultyMixAlwaysSumsToTotal(t *testing.T) {
	mixes := []map[string]float64{
		{DifficultyEasy: 0.3, DifficultyMedium: 0.5, DifficultyHard: 0.2},
		{DifficultyEasy: 1, DifficultyMedium: 1, DifficultyHard: 1},
		{DifficultyEasy: 0.7, DifficultyMedium: 0.3},
		{DifficultyEasy: 0.05, DifficultyMedium: 0.05, DifficultyHard: 0.9},
		{DifficultyEasy: 0.34, DifficultyMedium: 0.33, DifficultyHard: 0.33},
	}
	for _, mix := range mixes {
		for total := 1; total <= 40; total++ {
			got := AllocateDifficultyMix(total, mix)
			sum := 0
			for _, count := range got {
				if count < 0 {
					t.Fatalf("negative allocation %v for total=%d mix=%v", got, total, mix)
				}
				sum += count
			}
			if sum != total {
				t.Fatalf("allocation sum = %d, want %d (mix=%v, got=%v)", sum, total, mix, got)
			}
		}
	}
}

// mix 为空时回退到默认配比 0.3/0.5/0.2。
func TestAllocateDifficultyMixFallsBackToDefault(t *testing.T) {
	got := AllocateDifficultyMix(10, nil)
	want := map[string]int{DifficultyEasy: 3, DifficultyMedium: 5, DifficultyHard: 2}
	for level, count := range want {
		if got[level] != count {
			t.Fatalf("default allocation[%s] = %d, want %d (full: %v)", level, got[level], count, got)
		}
	}
}

// 无效配比（全 0、负值）同样回退到默认，不能返回全 0 分配。
func TestAllocateDifficultyMixRejectsInvalidMix(t *testing.T) {
	cases := []map[string]float64{
		{DifficultyEasy: 0, DifficultyMedium: 0, DifficultyHard: 0},
		{DifficultyEasy: -1, DifficultyMedium: 2},
		{"unknown_level": 1},
	}
	for _, mix := range cases {
		got := AllocateDifficultyMix(10, mix)
		sum := got[DifficultyEasy] + got[DifficultyMedium] + got[DifficultyHard]
		if sum != 10 {
			t.Fatalf("invalid mix %v produced sum=%d, want 10 (got=%v)", mix, sum, got)
		}
	}
}

// total <= 0 时返回全 0，不 panic。
func TestAllocateDifficultyMixHandlesZeroTotal(t *testing.T) {
	for _, total := range []int{0, -5} {
		got := AllocateDifficultyMix(total, map[string]float64{DifficultyEasy: 1})
		sum := got[DifficultyEasy] + got[DifficultyMedium] + got[DifficultyHard]
		if sum != 0 {
			t.Fatalf("total=%d produced sum=%d, want 0", total, sum)
		}
	}
}

// 模型返回的难度写法必须被归一化，无法识别的值不得丢弃问题（归为 medium）。
func TestReconcileDifficultyNormalizesModelOutput(t *testing.T) {
	cases := []struct {
		raw       string
		wantLevel string
		wantScore int
	}{
		{"easy", DifficultyEasy, 1},
		{"EASY", DifficultyEasy, 1},
		{" easy ", DifficultyEasy, 1},
		{"1", DifficultyEasy, 1},
		{"简单", DifficultyEasy, 1},
		{"medium", DifficultyMedium, 2},
		{"2", DifficultyMedium, 2},
		{"中等", DifficultyMedium, 2},
		{"hard", DifficultyHard, 3},
		{"HARD", DifficultyHard, 3},
		{"3", DifficultyHard, 3},
		{"困难", DifficultyHard, 3},
		{"", DifficultyMedium, 2},
		{"随便什么", DifficultyMedium, 2},
		{"4", DifficultyMedium, 2},
	}
	for _, item := range cases {
		level, score := ReconcileDifficulty(item.raw)
		if level != item.wantLevel || score != item.wantScore {
			t.Errorf("ReconcileDifficulty(%q) = (%s, %d), want (%s, %d)",
				item.raw, level, score, item.wantLevel, item.wantScore)
		}
	}
}

// 解析模型返回的对象数组形态。
func TestParseQuestionDraftsReadsStructuredObjects(t *testing.T) {
	raw := `[
      {"content":"在东海海域执行巡逻任务时遭遇不明目标，请做出规划。","difficulty":"hard"},
      {"content":"在近海航道发现可疑船只，请做出规划。","difficulty":"easy"}
    ]`
	drafts, err := parseQuestionDrafts(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(drafts) != 2 {
		t.Fatalf("got %d drafts, want 2", len(drafts))
	}
	if drafts[0].Difficulty != "hard" || drafts[1].Difficulty != "easy" {
		t.Fatalf("difficulty mismatch: %+v", drafts)
	}
}

// 兼容模型只返回字符串数组的形态。
func TestParseQuestionDraftsReadsPlainStrings(t *testing.T) {
	drafts, err := parseQuestionDrafts(`["问题甲","问题乙"]`)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(drafts) != 2 || drafts[0].Content != "问题甲" {
		t.Fatalf("unexpected drafts: %+v", drafts)
	}
}

// 解析要能穿透 Markdown 代码块与前后解释文字。
func TestParseQuestionDraftsToleratesFencesAndProse(t *testing.T) {
	raw := "好的，以下是问题：\n```json\n[{\"content\":\"场景问题\",\"difficulty\":\"medium\"}]\n```\n以上。"
	drafts, err := parseQuestionDrafts(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Content != "场景问题" {
		t.Fatalf("unexpected drafts: %+v", drafts)
	}
}

// 对象数组里 content 为空白的元素必须被剔除，且不能因此报错。
func TestParseQuestionDraftsSkipsEmptyContent(t *testing.T) {
	raw := `[{"content":"  ","difficulty":"easy"},{"content":"有效问题","difficulty":"hard"}]`
	drafts, err := parseQuestionDrafts(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(drafts) != 1 || drafts[0].Content != "有效问题" {
		t.Fatalf("unexpected drafts: %+v", drafts)
	}
}

// 完全无法解析时必须返回错误，不能静默返回空结果。
func TestParseQuestionDraftsErrorsOnGarbage(t *testing.T) {
	if _, err := parseQuestionDrafts("完全不是 JSON"); err == nil {
		t.Fatal("expected error for unparseable content")
	}
}

// remainingQuota 按计划扣减已产出数量。
func TestRemainingQuotaSubtractsProduced(t *testing.T) {
	allocation := map[string]int{DifficultyEasy: 3, DifficultyMedium: 5, DifficultyHard: 2}
	produced := map[string]int{DifficultyEasy: 3, DifficultyMedium: 1}
	quota := remainingQuota(allocation, produced, 6)
	if quota[DifficultyEasy] != 0 {
		t.Errorf("easy quota = %d, want 0 (already satisfied)", quota[DifficultyEasy])
	}
	if quota[DifficultyMedium] != 4 {
		t.Errorf("medium quota = %d, want 4", quota[DifficultyMedium])
	}
	if quota[DifficultyHard] != 2 {
		t.Errorf("hard quota = %d, want 2", quota[DifficultyHard])
	}
}

// 所有档位都已满足计划时，本轮按总缺口补齐而不是返回空配额。
func TestRemainingQuotaFallsBackWhenPlanSatisfied(t *testing.T) {
	allocation := map[string]int{DifficultyEasy: 1, DifficultyMedium: 1, DifficultyHard: 1}
	produced := map[string]int{DifficultyEasy: 5, DifficultyMedium: 5, DifficultyHard: 5}
	quota := remainingQuota(allocation, produced, 3)
	total := 0
	for _, count := range quota {
		total += count
	}
	if total != 3 {
		t.Fatalf("fallback quota total = %d, want 3 (quota=%v)", total, quota)
	}
}

// difficultyPlan 把配额展开成有序序列，顺序固定 easy→medium→hard。
func TestDifficultyPlanExpandsAllocationInFixedOrder(t *testing.T) {
	plan := difficultyPlan(map[string]int{
		DifficultyEasy: 3, DifficultyMedium: 5, DifficultyHard: 2,
	}, 10)
	if len(plan) != 10 {
		t.Fatalf("plan length = %d, want 10", len(plan))
	}
	counts := map[string]int{}
	for _, level := range plan {
		counts[level]++
	}
	if counts[DifficultyEasy] != 3 || counts[DifficultyMedium] != 5 || counts[DifficultyHard] != 2 {
		t.Fatalf("plan counts = %v, want easy=3 medium=5 hard=2", counts)
	}
	// 顺序必须可重现。
	if plan[0] != DifficultyEasy || plan[2] != DifficultyEasy || plan[3] != DifficultyMedium || plan[9] != DifficultyHard {
		t.Fatalf("plan order = %v, want easy×3 then medium×5 then hard×2", plan)
	}
}

// 配额总和小于 total 时用 medium 补齐，不得越界。
func TestDifficultyPlanPadsWhenAllocationShort(t *testing.T) {
	plan := difficultyPlan(map[string]int{DifficultyEasy: 1}, 4)
	if len(plan) != 4 {
		t.Fatalf("plan length = %d, want 4 (padded)", len(plan))
	}
	if plan[0] != DifficultyEasy {
		t.Fatalf("first entry should be easy, got %v", plan)
	}
}

// 契约样例：x=10、mix=0.3/0.5/0.2 → 计划序列恰好 3 easy / 5 medium / 2 hard。
func TestDifficultyPlanMatchesContractExample(t *testing.T) {
	allocation := AllocateDifficultyMix(10, map[string]float64{
		DifficultyEasy: 0.3, DifficultyMedium: 0.5, DifficultyHard: 0.2,
	})
	plan := difficultyPlan(allocation, 10)
	counts := map[string]int{}
	for _, level := range plan {
		counts[level]++
	}
	if counts[DifficultyEasy] != 3 || counts[DifficultyMedium] != 5 || counts[DifficultyHard] != 2 {
		t.Fatalf("contract example plan = %v, want easy=3 medium=5 hard=2", counts)
	}
}

// 生成器在方向为空、provider 不完整时必须明确报错。
func TestGenerateQuestionsV2ValidatesInput(t *testing.T) {
	ctx := testContext()
	provider := ProviderConfig{BaseURL: "http://example.invalid/v1", Model: "m", APIKey: "k"}

	if _, err := GenerateQuestionsV2(ctx, provider, QuestionGenInput{}); err == nil {
		t.Error("expected error when no directions provided")
	}

	withDirection := QuestionGenInput{
		DatasetID:             1,
		RootKeyword:           "海上巡逻",
		QuestionsPerDirection: 2,
		Directions:            []DirectionContext{{DomainID: 1, DomainName: "海上巡逻"}},
	}
	if _, err := GenerateQuestionsV2(ctx, ProviderConfig{ProviderType: "mock"}, withDirection); err == nil {
		t.Error("expected error for mock provider")
	}
	if _, err := GenerateQuestionsV2(ctx, ProviderConfig{BaseURL: "", APIKey: "k"}, withDirection); err == nil {
		t.Error("expected error for missing base url")
	}
	if _, err := GenerateQuestionsV2(ctx, ProviderConfig{BaseURL: "http://x", APIKey: ""}, withDirection); err == nil {
		t.Error("expected error for missing api key")
	}
}

// 长链标准步骤必须被渲染进提示词，且带判断点。
func TestBuildQuestionPromptIncludesChainFramework(t *testing.T) {
	input := QuestionGenInput{RootKeyword: "海上巡逻"}
	direction := DirectionContext{
		DomainName: "海上巡逻",
		ChainSteps: []model.ChainStep{
			{Title: "确认海域态势", Description: "收集海况与目标信息", Checkpoint: "态势是否清晰"},
			{Title: "评估可用兵力"},
		},
	}
	prompt := buildQuestionPrompt(input, direction, 3, map[string]int{DifficultyEasy: 1, DifficultyHard: 2})

	for _, want := range []string{"海上巡逻", "确认海域态势", "态势是否清晰", "评估可用兵力", "简单 1 个", "困难 2 个", "JSON"} {
		if !contains(prompt, want) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", want, prompt)
		}
	}
}

// testContext 返回一个已取消的上下文，使任何真实网络调用立即失败。
// 这些测试只验证入参校验，不应真的发出请求。
func testContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
