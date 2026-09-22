package model

import (
	"strings"
	"testing"
)

// 本文件验证 Issue #160 T18 的同基准比较判定（纯函数）。
//
// T18 最危险的错误是**方向写反**与**把「只有一侧」算进配对**：
// 前者让用户选错方案，后者会凭空制造差异。两者都不报错。

func score(value float64) *float64 { return &value }

func validBaseline() ComparisonBaseline {
	return ComparisonBaseline{
		Metric:       ComparisonMetricPaired,
		InputRef:     "questions-v1",
		SamplingSeed: 42,
		Judges:       []JudgeSpec{{ConnectionID: 9, EndpointFingerprint: "judge.example.com/v1"}},
		Rubric: RubricSpec{Dimensions: []RubricDimension{
			{Key: "accuracy", Label: "准确", Weight: 1, Min: 0, Max: 10},
		}},
	}
}

// TestBuildComparisonReportDeltaDirection 覆盖差异的**方向**语义。
//
// Delta = 右 - 左，正数表示右侧更好。方向写反是这类报告最危险的错误：
// 用户会据此选择一个更差的方案，而且看上去一切正常。
func TestBuildComparisonReportDeltaDirection(t *testing.T) {
	observations := []PairedObservation{
		{PairKey: "q1", LeftScore: score(4), RightScore: score(8)},
		{PairKey: "q2", LeftScore: score(6), RightScore: score(10)},
	}
	report := BuildComparisonReport(validBaseline(), observations)

	if report.PairedCount != 2 {
		t.Fatalf("应有 2 个配对完成，实际 %d", report.PairedCount)
	}
	if len(report.Dimensions) != 1 {
		t.Fatalf("应有 1 个维度，实际 %d", len(report.Dimensions))
	}
	dimension := report.Dimensions[0]
	if dimension.LeftMean != 5 || dimension.RightMean != 9 {
		t.Fatalf("两侧均值应为 5 / 9，实际 %v / %v", dimension.LeftMean, dimension.RightMean)
	}
	if dimension.Delta != 4 {
		t.Fatalf("Delta 应为右-左=+4（正数表示右侧更好），实际 %v", dimension.Delta)
	}
	if dimension.Pairs != 2 || dimension.Missing != 0 {
		t.Fatalf("配对数应为 2、缺分 0，实际 %d / %d", dimension.Pairs, dimension.Missing)
	}
}

// TestBuildComparisonReportExcludesSingleSidedObservations 覆盖
// 「只有一侧有的观测不得计入配对」。
//
// 把「只有一侧有分」的题算成 0（或算进配对）会凭空制造差异 ——
// 而那正是「拿两个任意批次相减」在逐题层面的形态。
func TestBuildComparisonReportExcludesSingleSidedObservations(t *testing.T) {
	observations := []PairedObservation{
		{PairKey: "q1", LeftScore: score(10), RightScore: score(10)},
		{PairKey: "q2", LeftScore: score(0), RightScore: nil},  // 只在左侧
		{PairKey: "q3", LeftScore: nil, RightScore: score(10)}, // 只在右侧
		{PairKey: "q4", LeftScore: nil, RightScore: nil},       // 两侧都没分
	}
	report := BuildComparisonReport(validBaseline(), observations)

	if report.PairedCount != 1 {
		t.Fatalf("只有 1 题两侧都有分，配对完成数应为 1，实际 %d", report.PairedCount)
	}
	if report.LeftOnlyCount != 1 || report.RightOnlyCount != 1 {
		t.Fatalf("两侧各 1 题单独出现，实际 %d / %d", report.LeftOnlyCount, report.RightOnlyCount)
	}
	dimension := report.Dimensions[0]
	if dimension.LeftMean != 10 || dimension.RightMean != 10 {
		t.Fatalf("均值只能统计配对观测（都应为 10），实际 %v / %v", dimension.LeftMean, dimension.RightMean)
	}
	if dimension.Delta != 0 {
		t.Fatalf("配对观测两侧相同，Delta 应为 0，实际 %v", dimension.Delta)
	}
	if dimension.Missing != 3 {
		t.Fatalf("3 个观测未配对（含两侧都缺的），实际 %d", dimension.Missing)
	}
	// 未配对必须在风险里说明，否则用户不知道样本量为何这么小。
	if !strings.Contains(strings.Join(report.Risks, " "), "未配对") {
		t.Fatalf("未配对的观测必须写进风险说明，实际 %v", report.Risks)
	}
}

// TestComparabilityIsExplicitAboutWhatCanBeClaimed 覆盖验收项
// 「基准不一致时拒绝『可比』标签」与「覆盖实验不得伪称逐题配对」。
func TestComparabilityIsExplicitAboutWhatCanBeClaimed(t *testing.T) {
	// 逐题配对：可以说「同一题在两方案下的差异」。
	paired := BuildComparisonReport(validBaseline(), []PairedObservation{
		{PairKey: "q1", LeftScore: score(5), RightScore: score(7)},
	})
	if !paired.Comparability.Comparable {
		t.Fatalf("逐题配对且有配对观测时应可比，实际 %s", paired.Comparability.Label)
	}
	if !strings.Contains(paired.Comparability.Label, "逐题配对") {
		t.Fatalf("标签必须说明口径，实际 %q", paired.Comparability.Label)
	}

	// 覆盖比较：两侧输入不同，**不得**声明逐题配对。
	coverageBaseline := validBaseline()
	coverageBaseline.Metric = ComparisonMetricCoverage
	coverageBaseline.InputRef = ""
	coverageBaseline.CoverageSlice = map[string]any{"domain": "cold-chain"}
	coverage := BuildComparisonReport(coverageBaseline, []PairedObservation{
		{PairKey: "d1", LeftScore: score(5), RightScore: score(7)},
	})
	if !strings.Contains(coverage.Comparability.Label, "输入不同") {
		t.Fatalf("覆盖比较的标签必须写明输入不同，实际 %q", coverage.Comparability.Label)
	}
	disclaimers := strings.Join(coverage.Comparability.Disclaimers, " ")
	if !strings.Contains(disclaimers, "不能") || !strings.Contains(disclaimers, "逐题配对") {
		t.Fatalf("必须显式禁止逐题配对的说法，实际 %q", disclaimers)
	}

	// 没有任何配对观测 → **不可比**（而不是「差异为 0」）。
	empty := BuildComparisonReport(validBaseline(), []PairedObservation{
		{PairKey: "q1", LeftScore: score(5), RightScore: nil},
	})
	if empty.Comparability.Comparable {
		t.Fatal("配对完成数为 0 时必须判为不可比（否则「差异 0」会被当成结论）")
	}
	if !strings.Contains(empty.Comparability.Label, "不可比较") {
		t.Fatalf("标签应明确说明不可比较，实际 %q", empty.Comparability.Label)
	}

	// 口径缺失 → 拒绝可比标签，不猜一个。
	unknown := validBaseline()
	unknown.Metric = "something-else"
	if report := BuildComparisonReport(unknown, []PairedObservation{
		{PairKey: "q1", LeftScore: score(5), RightScore: score(6)},
	}); report.Comparability.Comparable {
		t.Fatal("口径未知时不得给出「可比」标签")
	}
}

// TestComparabilityAlwaysDisclaimsSignificance 覆盖验收项
// 「不作无依据显著性或『最佳方案』断言」。
//
// 无论样本量大小，报告都必须写明它不提供显著性检验 ——
// 一个试制的样本量通常远不足以支撑这类结论，而给出数字会让人当成结论。
func TestComparabilityAlwaysDisclaimsSignificance(t *testing.T) {
	observations := []PairedObservation{}
	for index := 0; index < 200; index++ {
		observations = append(observations, PairedObservation{
			PairKey:    "q" + string(rune('a'+index%26)) + string(rune('0'+index/26)),
			LeftScore:  score(5),
			RightScore: score(6),
		})
	}
	report := BuildComparisonReport(validBaseline(), observations)
	disclaimers := strings.Join(report.Comparability.Disclaimers, " ")
	if !strings.Contains(disclaimers, "显著性") || !strings.Contains(disclaimers, "最佳方案") {
		t.Fatalf("必须显式声明不提供显著性与最佳方案断言，实际 %q", disclaimers)
	}

	// 小样本必须额外提示样本量不足。
	small := BuildComparisonReport(validBaseline(), []PairedObservation{
		{PairKey: "q1", LeftScore: score(5), RightScore: score(6)},
	})
	if !strings.Contains(strings.Join(small.Comparability.Disclaimers, " "), "样本量不足") {
		t.Fatalf("小样本必须提示样本量不足，实际 %q", small.Comparability.Disclaimers)
	}
}

// TestValidateComparisonBaseline 覆盖三条硬要求。
func TestValidateComparisonBaseline(t *testing.T) {
	left, right := int64(1), int64(2)
	valid := validBaseline()
	valid.LeftBatchID = &left
	valid.RightBatchID = &right
	if err := ValidateComparisonBaseline(valid); err != nil {
		t.Fatalf("合法基准不应报错：%v", err)
	}

	cases := []struct {
		name   string
		mutate func(b *ComparisonBaseline)
	}{
		{"缺一侧批次", func(b *ComparisonBaseline) { b.RightBatchID = nil }},
		{"两侧相同", func(b *ComparisonBaseline) { b.RightBatchID = b.LeftBatchID }},
		// 最重要的一条：声明逐题配对却不固定输入 = 「拿两个任意批次相减」。
		{"配对口径未固定输入", func(b *ComparisonBaseline) { b.InputRef = "" }},
		{"覆盖口径缺切片", func(b *ComparisonBaseline) {
			b.Metric = ComparisonMetricCoverage
			b.InputRef = ""
			b.CoverageSlice = nil
		}},
		{"口径非法", func(b *ComparisonBaseline) { b.Metric = "vibes" }},
		{"缺裁判", func(b *ComparisonBaseline) { b.Judges = nil }},
		{"量表不可用", func(b *ComparisonBaseline) { b.Rubric = RubricSpec{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			baseline := validBaseline()
			baseline.LeftBatchID = &left
			baseline.RightBatchID = &right
			testCase.mutate(&baseline)
			err := ValidateComparisonBaseline(baseline)
			if err == nil {
				t.Fatal("非法基准必须被拒绝")
			}
			if _, ok := HasFieldErrors(err); !ok {
				t.Fatalf("必须是字段级错误（界面据此聚焦字段），实际 %v", err)
			}
		})
	}
}

// TestValidateAdoptionRequiresReasonAndComparability 覆盖验收项
// 「采用 A/B 只更新指针并记录依据」。
func TestValidateAdoptionRequiresReasonAndComparability(t *testing.T) {
	comparable := BuildComparisonReport(validBaseline(), []PairedObservation{
		{PairKey: "q1", LeftScore: score(5), RightScore: score(7)},
	})
	if err := ValidateAdoption("right", "右侧在准确维度更好且成本相近", comparable); err != nil {
		t.Fatalf("合法采用不应报错：%v", err)
	}
	// 缺理由：没有依据的决定无法在以后复核。
	if err := ValidateAdoption("right", "  ", comparable); err == nil {
		t.Fatal("缺理由必须被拒绝")
	}
	// 侧别非法。
	if err := ValidateAdoption("middle", "理由", comparable); err == nil {
		t.Fatal("非法侧别必须被拒绝")
	}
	// 不可比的比较不能作为采用依据。
	incomparable := BuildComparisonReport(validBaseline(), []PairedObservation{
		{PairKey: "q1", LeftScore: score(5), RightScore: nil},
	})
	if err := ValidateAdoption("right", "理由", incomparable); err == nil {
		t.Fatal("不可比的比较不得作为采用依据")
	}
}

// TestComparisonCapabilitiesRequiresComparable 覆盖「不可比时不能采用」。
func TestComparisonCapabilitiesRequiresComparable(t *testing.T) {
	if ComparisonCapabilities(ProjectRoleOwner, false, false).CanRun {
		t.Fatal("不可比时不得提供运行/采用能力")
	}
	if !ComparisonCapabilities(ProjectRoleOwner, true, false).CanRun {
		t.Fatal("可比且未采用时应可运行扩量规划")
	}
	if ComparisonCapabilities(ProjectRoleOwner, true, true).CanRun {
		t.Fatal("已采用后不得再次采用（改方案要重新比较）")
	}
	if ComparisonCapabilities(ProjectRoleReviewer, true, false).CanRun {
		t.Fatal("reviewer 不得采用方案")
	}
}
