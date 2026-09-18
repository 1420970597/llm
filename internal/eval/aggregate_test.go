package eval

import (
	"math"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件的测试全部针对纯函数，不碰 DB、不调 LLM。
//
// 这些统计口径直接决定报告结论，一旦算错用户会得到错误的判断，
// 因此每条口径都用「能区分对错的具体数值」锁定，而不是只断言非空。

// approxEqual 浮点比较。
func approxEqual(left, right, tolerance float64) bool {
	return math.Abs(left-right) <= tolerance
}

// testDimensions 构造两个权重不同、量表区间相同的维度。
//
// 权重刻意设为 3:1，这样加权平均 (0.75) 与算术平均 (0.5) 有明显差异，
// 能真正验证「用了权重」而不是碰巧相等。
func testDimensions() []model.EvalDimension {
	return []model.EvalDimension{
		{Key: "long_chain_depth", Name: "长链深度", Category: "long_chain", Weight: 3, ScaleMin: 0, ScaleMax: 10},
		{Key: "answer_accuracy", Name: "答案准确性", Category: "answer_quality", Weight: 1, ScaleMin: 0, ScaleMax: 10},
	}
}

// score 构造一条打分记录。
func score(itemID, providerID int64, dimensionKey string, value float64) model.EvalItemScore {
	return model.EvalItemScore{
		EvalRunID:       1,
		EvalItemID:      itemID,
		JudgeProviderID: providerID,
		DimensionKey:    dimensionKey,
		Score:           value,
		Status:          "scored",
	}
}

// item 构造一条被评条目。
func item(id int64, index int) model.EvalItem {
	return model.EvalItem{ID: id, EvalRunID: 1, DatasetID: 1, QuestionID: id * 10, ItemIndex: index}
}

// TestAggregateUsesDimensionWeight 验证总分是加权平均而非算术平均。
//
// 数据：维度 A（权重 3）均分 10，维度 B（权重 1）均分 0。
// 加权 = (10*3 + 0*1) / 4 = 7.5；算术 = (10+0)/2 = 5。
// 若实现退回算术平均，这个断言会失败。
func TestAggregateUsesDimensionWeight(t *testing.T) {
	report, _ := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      []model.EvalItem{item(1, 0)},
		Dimensions: testDimensions(),
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 10),
			score(1, 100, "answer_accuracy", 0),
		},
	})

	if !approxEqual(report.OverallScore, 7.5, 1e-9) {
		t.Fatalf("加权总分应为 7.5（权重 3:1），实际 %.4f", report.OverallScore)
	}
	if approxEqual(report.OverallScore, 5.0, 1e-9) {
		t.Fatal("总分等于算术平均 5.0，说明维度权重未生效")
	}
}

// TestAggregateSkipsZeroWeightDimensions 验证权重为 0 的维度被排除且被记录。
func TestAggregateSkipsZeroWeightDimensions(t *testing.T) {
	dimensions := testDimensions()
	dimensions[1].Weight = 0 // answer_accuracy 权重置 0

	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      []model.EvalItem{item(1, 0)},
		Dimensions: dimensions,
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 10),
			score(1, 100, "answer_accuracy", 0),
		},
	})

	if !approxEqual(report.OverallScore, 10, 1e-9) {
		t.Fatalf("权重 0 的维度应被排除，总分应为 10，实际 %.4f", report.OverallScore)
	}
	if len(notes.ZeroWeightDimensions) != 1 || notes.ZeroWeightDimensions[0] != "answer_accuracy" {
		t.Fatalf("应记录被排除的维度 answer_accuracy，实际 %v", notes.ZeroWeightDimensions)
	}
	// 被排除的维度分数仍要单独展示，不能从报告里消失。
	if len(report.Dimensions) != 2 {
		t.Fatalf("两个维度都应出现在维度统计里，实际 %d 个", len(report.Dimensions))
	}
}

// TestAggregateDimensionStats 验证逐维度统计的均分、标准差与极值。
//
// 数据 2、4、4、4、5、5、7、9：均值 5，总体标准差 2。
func TestAggregateDimensionStats(t *testing.T) {
	values := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	scores := make([]model.EvalItemScore, 0, len(values))
	items := make([]model.EvalItem, 0, len(values))
	for index, value := range values {
		id := int64(index + 1)
		items = append(items, item(id, index))
		scores = append(scores, score(id, 100, "long_chain_depth", value))
	}

	report, _ := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
	})

	if len(report.Dimensions) != 1 {
		t.Fatalf("应只有 1 个维度有分数，实际 %d 个", len(report.Dimensions))
	}
	stat := report.Dimensions[0]
	if !approxEqual(stat.Score, 5, 1e-9) {
		t.Errorf("均分应为 5，实际 %.4f", stat.Score)
	}
	if !approxEqual(stat.StdDev, 2, 1e-9) {
		t.Errorf("总体标准差应为 2，实际 %.4f", stat.StdDev)
	}
	if stat.Min != 2 || stat.Max != 9 {
		t.Errorf("极值应为 2~9，实际 %.2f~%.2f", stat.Min, stat.Max)
	}
	if stat.SampleCount != 8 {
		t.Errorf("样本数应为 8，实际 %d", stat.SampleCount)
	}
}

// TestStdDevSingleSampleIsZero 锁定 n=1 的边界：必须返回 0 而不是 NaN。
//
// 返回 NaN 会让前端渲染出 "NaN"，且 NaN 参与后续比较会静默失效。
func TestStdDevSingleSampleIsZero(t *testing.T) {
	if got := stdDev([]float64{7}); got != 0 {
		t.Fatalf("单样本标准差应为 0，实际 %v", got)
	}
	if got := stdDev([]float64{7}); math.IsNaN(got) {
		t.Fatal("单样本标准差返回了 NaN")
	}
	if got := stdDev(nil); got != 0 {
		t.Fatalf("空输入标准差应为 0，实际 %v", got)
	}
}

// TestAggregateEmptyInputDoesNotPanic 空输入必须安全返回空报告。
func TestAggregateEmptyInputDoesNotPanic(t *testing.T) {
	report, notes := Aggregate(AggregateInput{Run: model.EvalRun{ID: 1}})

	if report.OverallScore != 0 {
		t.Errorf("空输入总分应为 0，实际 %v", report.OverallScore)
	}
	if report.SampleCount != 0 {
		t.Errorf("空输入样本数应为 0，实际 %d", report.SampleCount)
	}
	if report.JudgeAgreement != JudgeAgreementNotApplicable {
		t.Errorf("空输入一致性应为不适用哨兵值 %.1f，实际 %v",
			JudgeAgreementNotApplicable, report.JudgeAgreement)
	}
	if report.Judges == nil || report.Dimensions == nil || report.WeakestItems == nil {
		t.Error("空输入应返回空切片而非 nil，否则 JSON 序列化成 null，前端渲染会崩")
	}
	if len(notes.ZeroWeightDimensions) != 0 {
		t.Errorf("空输入不应有被排除的维度，实际 %v", notes.ZeroWeightDimensions)
	}
}

// TestJudgeAgreementIdenticalJudges 两个完全一致的裁判 → 一致性最高（1.0）。
func TestJudgeAgreementIdenticalJudges(t *testing.T) {
	items := []model.EvalItem{item(1, 0), item(2, 1), item(3, 2), item(4, 3)}
	scores := []model.EvalItemScore{}
	for index, value := range []float64{1, 3, 7, 9} {
		id := int64(index + 1)
		scores = append(scores,
			score(id, 100, "long_chain_depth", value),
			score(id, 200, "long_chain_depth", value),
		)
	}

	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "A"},
			{ProviderID: 200, ProviderName: "B"},
		},
	})

	if !approxEqual(report.JudgeAgreement, 1.0, 1e-9) {
		t.Fatalf("两个完全一致的裁判一致性应为 1.0，实际 %.6f", report.JudgeAgreement)
	}
	if notes.AgreementPairs != 1 {
		t.Errorf("应有 1 对裁判参与比较，实际 %d", notes.AgreementPairs)
	}
}

// TestJudgeAgreementOppositeJudges 两个排序完全相反的裁判 → 一致性最低（0.0）。
func TestJudgeAgreementOppositeJudges(t *testing.T) {
	items := []model.EvalItem{item(1, 0), item(2, 1), item(3, 2), item(4, 3)}
	scores := []model.EvalItemScore{}
	values := []float64{1, 3, 7, 9}
	for index, value := range values {
		id := int64(index + 1)
		scores = append(scores,
			score(id, 100, "long_chain_depth", value),
			score(id, 200, "long_chain_depth", values[len(values)-1-index]),
		)
	}

	report, _ := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "A"},
			{ProviderID: 200, ProviderName: "B"},
		},
	})

	if !approxEqual(report.JudgeAgreement, 0.0, 1e-9) {
		t.Fatalf("排序完全相反的裁判一致性应为 0.0，实际 %.6f", report.JudgeAgreement)
	}
}

// TestJudgeAgreementIgnoresSystematicOffset 锁定算法选择的关键性质。
//
// 裁判 B 对每条数据都比 A 高 3 分，但两人的**排序完全一致**。
// 这属于「打分尺度不同」，不是「判断分歧」，一致性应当为 1.0。
//
// 若实现改用「平均绝对差」，这里会得到一个明显低于 1 的值 ——
// 这条测试就是防止有人把算法换成混淆两者的口径。
func TestJudgeAgreementIgnoresSystematicOffset(t *testing.T) {
	items := []model.EvalItem{item(1, 0), item(2, 1), item(3, 2), item(4, 3)}
	scores := []model.EvalItemScore{}
	for index, value := range []float64{1, 3, 7, 9} {
		id := int64(index + 1)
		scores = append(scores,
			score(id, 100, "long_chain_depth", value),
			score(id, 200, "long_chain_depth", value+3),
		)
	}

	report, _ := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "A"},
			{ProviderID: 200, ProviderName: "B"},
		},
	})

	if !approxEqual(report.JudgeAgreement, 1.0, 1e-9) {
		t.Fatalf("仅存在系统性偏移（排序一致）时一致性应为 1.0，实际 %.6f"+
			" —— 秩相关可能被换成了绝对差", report.JudgeAgreement)
	}
}

// TestJudgeAgreementSingleJudgeIsNotApplicable 锁定单裁判语义。
//
// 单个裁判时一致性没有定义。返回 0 会被读成「完全不一致」（相反结论），
// 因此必须返回区间外的哨兵值 JudgeAgreementNotApplicable。
func TestJudgeAgreementSingleJudgeIsNotApplicable(t *testing.T) {
	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      []model.EvalItem{item(1, 0), item(2, 1)},
		Dimensions: testDimensions(),
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 5),
			score(2, 100, "long_chain_depth", 8),
		},
		Judges: []model.EvalRunJudge{{ProviderID: 100, ProviderName: "唯一裁判"}},
	})

	if report.JudgeAgreement != JudgeAgreementNotApplicable {
		t.Fatalf("单裁判一致性应为不适用哨兵值 %.1f，实际 %.4f",
			JudgeAgreementNotApplicable, report.JudgeAgreement)
	}
	if report.JudgeAgreement == 0 {
		t.Fatal("单裁判返回了 0 —— 会被前端误读成「完全不一致」")
	}
	if notes.AgreementPairs != 0 {
		t.Errorf("单裁判不应有参与比较的裁判对，实际 %d", notes.AgreementPairs)
	}
	if len(notes.AgreementSkipped) == 0 {
		t.Error("单裁判必须记录跳过原因，否则用户无法知道一致性为何缺失")
	}
}

// TestJudgeAgreementConstantScores 某裁判给所有数据同分时秩相关无定义。
//
// 此时必须返回哨兵值并记录原因，不能编造一个数值。
func TestJudgeAgreementConstantScores(t *testing.T) {
	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      []model.EvalItem{item(1, 0), item(2, 1), item(3, 2)},
		Dimensions: testDimensions(),
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 5),
			score(2, 100, "long_chain_depth", 5),
			score(3, 100, "long_chain_depth", 5),
			score(1, 200, "long_chain_depth", 2),
			score(2, 200, "long_chain_depth", 6),
			score(3, 200, "long_chain_depth", 9),
		},
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "恒定"},
			{ProviderID: 200, ProviderName: "有变化"},
		},
	})

	if report.JudgeAgreement != JudgeAgreementNotApplicable {
		t.Fatalf("一侧打分无变化时一致性无定义，应为哨兵值，实际 %.4f", report.JudgeAgreement)
	}
	if len(notes.AgreementSkipped) == 0 {
		t.Error("必须记录「打分无变化导致无定义」的原因")
	}
}

// TestAggregateExcludesFailedScores 状态非 scored 的记录不参与统计。
//
// 失败记录的分值为 0，若计入会凭空拉低均分。
func TestAggregateExcludesFailedScores(t *testing.T) {
	failed := score(2, 100, "long_chain_depth", 0)
	failed.Status = "failed"

	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      []model.EvalItem{item(1, 0), item(2, 1)},
		Dimensions: testDimensions(),
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 10),
			failed,
		},
	})

	if !approxEqual(report.OverallScore, 10, 1e-9) {
		t.Fatalf("失败记录应被排除，总分应为 10，实际 %.4f", report.OverallScore)
	}
	if notes.FailedScores != 1 {
		t.Errorf("应记录 1 条失败打分，实际 %d", notes.FailedScores)
	}
}

// TestAggregateWeakestItems 验证最弱条目按分数升序、且截断到上限。
func TestAggregateWeakestItems(t *testing.T) {
	items := []model.EvalItem{}
	scores := []model.EvalItemScore{}
	for index := 0; index < 15; index++ {
		id := int64(index + 1)
		items = append(items, item(id, index))
		// 第 1 条最低（1 分），依次递增到 15 分。
		scores = append(scores, score(id, 100, "long_chain_depth", float64(index+1)))
	}

	report, _ := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
	})

	if len(report.WeakestItems) != WeakestItemLimit {
		t.Fatalf("最弱条目应截断到 %d 条，实际 %d", WeakestItemLimit, len(report.WeakestItems))
	}
	if report.WeakestItems[0].Score != 1 {
		t.Errorf("最弱条目第一条应为 1 分，实际 %.2f", report.WeakestItems[0].Score)
	}
	for index := 1; index < len(report.WeakestItems); index++ {
		if report.WeakestItems[index].Score < report.WeakestItems[index-1].Score {
			t.Fatalf("最弱条目必须按分数升序，第 %d 条 %.2f 小于前一条 %.2f",
				index, report.WeakestItems[index].Score, report.WeakestItems[index-1].Score)
		}
	}
}

// TestAggregateJudgeStats 验证逐裁判统计包含均分与逐条分数。
func TestAggregateJudgeStats(t *testing.T) {
	report, _ := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed"},
		Items:      []model.EvalItem{item(1, 0), item(2, 1)},
		Dimensions: testDimensions(),
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 2),
			score(2, 100, "long_chain_depth", 8), // 裁判 100 均分 5
			score(1, 200, "long_chain_depth", 6),
			score(2, 200, "long_chain_depth", 10), // 裁判 200 均分 8
		},
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "A"},
			{ProviderID: 200, ProviderName: "B"},
		},
	})

	if len(report.Judges) != 2 {
		t.Fatalf("应有 2 个裁判统计，实际 %d", len(report.Judges))
	}
	// 按 provider id 升序，因此 [0] 是 100。
	if report.Judges[0].ProviderID != 100 || !approxEqual(report.Judges[0].Score, 5, 1e-9) {
		t.Errorf("裁判 100 均分应为 5，实际 %v/%.2f", report.Judges[0].ProviderID, report.Judges[0].Score)
	}
	if !approxEqual(report.Judges[1].Score, 8, 1e-9) {
		t.Errorf("裁判 200 均分应为 8，实际 %.2f", report.Judges[1].Score)
	}
	if len(report.Judges[0].ItemScores) != 2 {
		t.Errorf("裁判 100 应有 2 条逐条分数，实际 %d", len(report.Judges[0].ItemScores))
	}
	if len(report.Judges[0].Dimensions) != 1 {
		t.Errorf("裁判 100 应有 1 个维度明细，实际 %d", len(report.Judges[0].Dimensions))
	}
}

// TestNormalizeScore 验证按维度量表区间归一化。
func TestNormalizeScore(t *testing.T) {
	dimension := model.EvalDimension{ScaleMin: 0, ScaleMax: 10}

	cases := []struct {
		score    float64
		expected float64
	}{
		{0, 0},
		{5, 0.5},
		{10, 1},
		{-5, 0}, // 越界下界夹紧
		{15, 1}, // 越界上界夹紧
	}
	for _, tc := range cases {
		got, ok := normalizeScore(tc.score, dimension)
		if !ok {
			t.Fatalf("量表 0~10 应可归一化，score=%.1f 返回 ok=false", tc.score)
		}
		if !approxEqual(got, tc.expected, 1e-9) {
			t.Errorf("score=%.1f 归一化应为 %.2f，实际 %.4f", tc.score, tc.expected, got)
		}
	}

	// 量表区间非法时必须返回 ok=false，不能猜一个区间出来。
	if _, ok := normalizeScore(5, model.EvalDimension{ScaleMin: 10, ScaleMax: 10}); ok {
		t.Error("区间为 0 时不应归一化（会除零）")
	}
}

// TestSpearmanWithTies 并列分数使用平均秩，仍能得到正确相关系数。
func TestSpearmanWithTies(t *testing.T) {
	left := []float64{1, 2, 2, 3}
	right := []float64{1, 2, 2, 3}

	rho, ok := spearman(left, right)
	if !ok {
		t.Fatal("完全相同的序列（含并列）应可计算")
	}
	if !approxEqual(rho, 1.0, 1e-9) {
		t.Fatalf("相同序列 Spearman 应为 1.0，实际 %.6f", rho)
	}
}

// TestSpearmanUndefinedCases 长度不足或无变化时必须返回 ok=false。
func TestSpearmanUndefinedCases(t *testing.T) {
	if _, ok := spearman([]float64{1}, []float64{2}); ok {
		t.Error("长度为 1 时秩相关无定义")
	}
	if _, ok := spearman([]float64{1, 2}, []float64{1, 2, 3}); ok {
		t.Error("长度不等时不应计算")
	}
	if _, ok := spearman([]float64{5, 5, 5}, []float64{1, 2, 3}); ok {
		t.Error("一侧无变化时不应计算（分母为 0）")
	}
}
