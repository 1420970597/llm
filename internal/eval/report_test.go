package eval

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件测试结论生成。
//
// 结论是给用户看的最终判断，因此测试重点不是「有没有文字」，
// 而是「文字是否由真实统计量推导」—— 尤其：
//   - 低一致性必须出现分歧警告（多 LLM 评估最重要的诚实性输出）
//   - 裁判偏差必须点名具体裁判
//   - 降级情况（权重为 0 的维度被排除等）必须告知用户

// joined 把结论拼成一个字符串，便于做包含性断言。
func joined(conclusions []string) string {
	return strings.Join(conclusions, "\n")
}

// highQualityInput 构造一份各维度都高分的输入（归一化总分接近 1）。
func highQualityInput() AggregateInput {
	items := []model.EvalItem{}
	scores := []model.EvalItemScore{}
	for index := 0; index < 12; index++ {
		id := int64(index + 1)
		items = append(items, item(id, index))
		scores = append(scores,
			score(id, 100, "long_chain_depth", 9),
			score(id, 100, "answer_accuracy", 9),
			score(id, 200, "long_chain_depth", 9),
			score(id, 200, "answer_accuracy", 9),
		)
	}
	return AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 12, ScoredItems: 12},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "裁判甲"},
			{ProviderID: 200, ProviderName: "裁判乙"},
		},
	}
}

// TestConclusionsHighQualityIsPositive 高分数据 → 结论含正向判断。
func TestConclusionsHighQualityIsPositive(t *testing.T) {
	report, notes := Aggregate(highQualityInput())
	conclusions := BuildConclusions(report, notes, "completed")

	text := joined(conclusions)
	if !strings.Contains(text, "优秀") {
		t.Fatalf("归一化得分 %.2f 应给出「优秀」判断，实际结论：\n%s",
			notes.NormalizedOverall, text)
	}
	// 高分数据不应出现「不达标」这类负向措辞。
	if strings.Contains(text, "不达标") {
		t.Errorf("高分数据不应出现「不达标」，实际结论：\n%s", text)
	}
}

// TestConclusionsLowQualityGivesAdvice 低分数据 → 结论含改进建议与最弱维度。
func TestConclusionsLowQualityGivesAdvice(t *testing.T) {
	input := highQualityInput()
	// 把 answer_accuracy 压到很低（权重 1），long_chain_depth 保持中低（权重 3）。
	scores := []model.EvalItemScore{}
	for index, value := range input.Scores {
		value.Score = 3
		if value.DimensionKey == "answer_accuracy" {
			value.Score = 0.5
		}
		scores = append(scores, value)
		_ = index
	}
	input.Scores = scores

	report, notes := Aggregate(input)
	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	// 归一化总分约 0.19，应落入「偏低」档。
	if !strings.Contains(text, "偏低") {
		t.Errorf("低分数据应给出「偏低」判断，实际结论：\n%s", text)
	}
	// 必须点名最弱维度「答案准确性」。
	if !strings.Contains(text, "答案准确性") {
		t.Errorf("结论应点名最弱维度「答案准确性」，实际结论：\n%s", text)
	}
	// 必须给出可操作建议。
	if !strings.Contains(text, "建议") {
		t.Errorf("结论应包含可操作建议，实际结论：\n%s", text)
	}
}

// TestConclusionsLowAgreementWarns 低一致性 → 必须出现明确的分歧警告。
//
// 这是本 lane 最重要的诚实性输出：各裁判分歧大时，平均分掩盖了判断差异，
// 用户必须被告知结论可信度受限。
func TestConclusionsLowAgreementWarns(t *testing.T) {
	items := []model.EvalItem{}
	scores := []model.EvalItemScore{}
	// 裁判甲升序、裁判乙降序 —— 排序完全相反，一致性 0。
	values := []float64{1, 3, 5, 7, 9, 11}
	for index := 0; index < len(values); index++ {
		id := int64(index + 1)
		items = append(items, item(id, index))
		scores = append(scores,
			score(id, 100, "long_chain_depth", values[index]),
			score(id, 200, "long_chain_depth", values[len(values)-1-index]),
		)
	}

	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 6, ScoredItems: 6},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "裁判甲"},
			{ProviderID: 200, ProviderName: "裁判乙"},
		},
	})

	if report.JudgeAgreement > agreementLow {
		t.Fatalf("测试数据应产生低一致性，实际 %.4f", report.JudgeAgreement)
	}

	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	if !strings.Contains(text, "分歧") {
		t.Fatalf("低一致性必须给出分歧警告，实际结论：\n%s", text)
	}
	if !strings.Contains(text, "可信度受限") {
		t.Errorf("低一致性警告应明确说明结论可信度受限，实际结论：\n%s", text)
	}
}

// TestConclusionsSingleJudgeExplainsMissingAgreement 单裁判 → 说明一致性为何缺失。
//
// 必须解释「为什么没有一致性指标」，且不能把它说成「不一致」。
func TestConclusionsSingleJudgeExplainsMissingAgreement(t *testing.T) {
	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 3, ScoredItems: 3},
		Items:      []model.EvalItem{item(1, 0), item(2, 1), item(3, 2)},
		Dimensions: testDimensions(),
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 5),
			score(2, 100, "long_chain_depth", 7),
			score(3, 100, "long_chain_depth", 9),
		},
		Judges: []model.EvalRunJudge{{ProviderID: 100, ProviderName: "唯一裁判"}},
	})

	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	if !strings.Contains(text, "不适用") {
		t.Errorf("单裁判应说明一致性不适用，实际结论：\n%s", text)
	}
	if !strings.Contains(text, "1 个裁判") {
		t.Errorf("应说明原因是只有 1 个裁判，实际结论：\n%s", text)
	}
	// 绝不能把「无法计算」表述成「不一致」。
	if strings.Contains(text, "分歧较大") {
		t.Errorf("单裁判时不应出现「分歧较大」（那是相反的结论），实际结论：\n%s", text)
	}
}

// TestConclusionsNamesBiasedJudge 裁判系统性偏差 → 点名该裁判。
func TestConclusionsNamesBiasedJudge(t *testing.T) {
	items := []model.EvalItem{}
	scores := []model.EvalItemScore{}
	// 裁判甲打分正常（4~9），裁判乙对所有数据都打满分（10）—— 系统性偏高。
	values := []float64{4, 5, 6, 7, 8, 9}
	for index := 0; index < len(values); index++ {
		id := int64(index + 1)
		items = append(items, item(id, index))
		scores = append(scores,
			score(id, 100, "long_chain_depth", values[index]),
			score(id, 200, "long_chain_depth", 10),
		)
	}

	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 6, ScoredItems: 6},
		Items:      items,
		Dimensions: testDimensions(),
		Scores:     scores,
		Judges: []model.EvalRunJudge{
			{ProviderID: 100, ProviderName: "严格裁判", Model: "model-a"},
			{ProviderID: 200, ProviderName: "宽松裁判", Model: "model-b"},
		},
	})

	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	if !strings.Contains(text, "宽松裁判") {
		t.Fatalf("应点名打分偏高的裁判「宽松裁判」，实际结论：\n%s", text)
	}
	if !strings.Contains(text, "偏高") {
		t.Errorf("应指出该裁判打分偏高，实际结论：\n%s", text)
	}
}

// TestConclusionsReportZeroWeightDimension 权重为 0 的维度被排除时必须告知用户。
//
// 用户配了 0 权重却发现分数没变，若无一行说明只会以为系统算错了。
func TestConclusionsReportZeroWeightDimension(t *testing.T) {
	dimensions := testDimensions()
	dimensions[1].Weight = 0

	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 3, ScoredItems: 3},
		Items:      []model.EvalItem{item(1, 0), item(2, 1), item(3, 2)},
		Dimensions: dimensions,
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 8),
			score(2, 100, "long_chain_depth", 8),
			score(3, 100, "long_chain_depth", 8),
			score(1, 100, "answer_accuracy", 2),
			score(2, 100, "answer_accuracy", 2),
			score(3, 100, "answer_accuracy", 2),
		},
	})

	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	if !strings.Contains(text, "answer_accuracy") {
		t.Fatalf("应告知用户 answer_accuracy 被排除在总分外，实际结论：\n%s", text)
	}
	if !strings.Contains(text, "权重") {
		t.Errorf("应说明原因是权重为 0，实际结论：\n%s", text)
	}
}

// TestConclusionsReportFailedScores 失败打分被排除时必须告知条数。
func TestConclusionsReportFailedScores(t *testing.T) {
	failed := score(2, 100, "long_chain_depth", 0)
	failed.Status = "failed"

	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 2, ScoredItems: 1},
		Items:      []model.EvalItem{item(1, 0), item(2, 1)},
		Dimensions: testDimensions(),
		Scores:     []model.EvalItemScore{score(1, 100, "long_chain_depth", 8), failed},
	})

	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	if !strings.Contains(text, "1 条打分未成功") {
		t.Fatalf("应告知有 1 条打分失败，实际结论：\n%s", text)
	}
}

// TestConclusionsIncompleteRunGivesNoQualityVerdict 未完成的 run 不给质量结论。
//
// 这是诚实性硬要求：部分评分不足以代表整个数据集，
// 此时输出「质量优秀」会误导用户直接把数据投入训练。
func TestConclusionsIncompleteRunGivesNoQualityVerdict(t *testing.T) {
	input := highQualityInput()
	input.Run.Status = "running"

	report, notes := Aggregate(input)
	conclusions := BuildConclusions(report, notes, "running")
	text := joined(conclusions)

	if !strings.Contains(text, "尚未完成") {
		t.Fatalf("未完成时应说明状态，实际结论：\n%s", text)
	}
	if !strings.Contains(text, "running") {
		t.Errorf("应写出当前具体状态，实际结论：\n%s", text)
	}
	// 即使底层数据全是高分，也不能给质量判断。
	if strings.Contains(text, "优秀") {
		t.Fatalf("未完成的评估不应给出质量结论，实际结论：\n%s", text)
	}
}

// TestConclusionsEmptyScoresIsExplicit 无任何分数时结论必须明确指出。
func TestConclusionsEmptyScoresIsExplicit(t *testing.T) {
	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 5, ScoredItems: 0},
		Dimensions: testDimensions(),
	})

	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	if !strings.Contains(text, "没有任何成功评分的条目") {
		t.Fatalf("无分数时应明确指出，实际结论：\n%s", text)
	}
}

// TestConclusionsSmallSampleWarns 样本量过小时必须警示结论不稳。
func TestConclusionsSmallSampleWarns(t *testing.T) {
	report, notes := Aggregate(AggregateInput{
		Run:        model.EvalRun{ID: 1, Status: "completed", TotalItems: 3, ScoredItems: 3},
		Items:      []model.EvalItem{item(1, 0), item(2, 1), item(3, 2)},
		Dimensions: testDimensions(),
		Scores: []model.EvalItemScore{
			score(1, 100, "long_chain_depth", 8),
			score(2, 100, "long_chain_depth", 8),
			score(3, 100, "long_chain_depth", 8),
		},
	})

	conclusions := BuildConclusions(report, notes, "completed")
	text := joined(conclusions)

	if !strings.Contains(text, "样本量偏小") {
		t.Fatalf("3 条样本应警示样本量偏小，实际结论：\n%s", text)
	}
}

// TestBuildSummariesCoversAllScopes 汇总行覆盖 overall / judge / dimension / item。
func TestBuildSummariesCoversAllScopes(t *testing.T) {
	report, _ := Aggregate(highQualityInput())
	report.DatasetName = "测试数据集"
	summaries := BuildSummaries(report)

	scopes := map[string]int{}
	for _, summary := range summaries {
		if summary.EvalRunID != report.EvalRun.ID {
			t.Errorf("汇总行的 evalRunId 应为 %d，实际 %d", report.EvalRun.ID, summary.EvalRunID)
		}
		scopes[summary.Scope]++
	}

	for _, expected := range []string{"overall", "judge", "dimension", "item"} {
		if scopes[expected] == 0 {
			t.Errorf("缺少 scope=%s 的汇总行，实际分布 %v", expected, scopes)
		}
	}
	if scopes["overall"] != 1 {
		t.Errorf("overall 只应有 1 行，实际 %d", scopes["overall"])
	}
	if scopes["judge"] != 2 {
		t.Errorf("judge 应有 2 行（2 个裁判），实际 %d", scopes["judge"])
	}
	if scopes["dimension"] != 2 {
		t.Errorf("dimension 应有 2 行（2 个维度），实际 %d", scopes["dimension"])
	}

	// overall 行要带上一致性，否则前端拿不到这个关键指标。
	for _, summary := range summaries {
		if summary.Scope != "overall" {
			continue
		}
		if _, ok := summary.Detail["judgeAgreement"]; !ok {
			t.Error("overall 汇总行必须包含 judgeAgreement")
		}
	}
}

// TestBuildSummariesStableOrder 汇总行顺序必须稳定，便于比对与去重。
func TestBuildSummariesStableOrder(t *testing.T) {
	report, _ := Aggregate(highQualityInput())

	first := BuildSummaries(report)
	second := BuildSummaries(report)

	if len(first) != len(second) {
		t.Fatalf("两次调用行数应一致：%d vs %d", len(first), len(second))
	}
	for index := range first {
		if first[index].Scope != second[index].Scope || first[index].RefKey != second[index].RefKey {
			t.Fatalf("第 %d 行顺序不稳定：%s/%s vs %s/%s",
				index, first[index].Scope, first[index].RefKey, second[index].Scope, second[index].RefKey)
		}
	}
}

// TestCategoryLabelFallsBackToKey 未知分类回退为 key 本身，不返回空字符串。
func TestCategoryLabelFallsBackToKey(t *testing.T) {
	if got := categoryLabel("long_chain"); got != "长链思考" {
		t.Errorf("long_chain 应译为「长链思考」，实际 %q", got)
	}
	if got := categoryLabel("unknown_category"); got != "unknown_category" {
		t.Errorf("未知分类应回退为 key 本身，实际 %q", got)
	}
	if got := categoryLabel(""); got != "未分类" {
		t.Errorf("空分类应显示「未分类」，实际 %q", got)
	}
}

// TestCategoryLabelCoversAllBuiltinCategories 每个内置分类都必须有中文名与专属建议。
//
// 内置 58 个维度分布在 7 个分类里；漏掉任何一个，中文结论里就会混进
// 英文 key（如「domain_fit 类」），并回退到通用建议。
func TestCategoryLabelCoversAllBuiltinCategories(t *testing.T) {
	for _, category := range Categories() {
		if got := categoryLabel(category); got == category {
			t.Errorf("内置分类 %q 缺少中文名，结论里会直接显示英文 key", category)
		}
		advice := dimensionAdvice(model.EvalDimensionStat{Category: category})
		if advice == genericDimensionAdvice {
			t.Errorf("内置分类 %q 缺少专属建议，回退到了通用文案", category)
		}
	}
}
