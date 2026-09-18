package eval

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
)

// 本文件把 Aggregate 算出的统计量翻译成给用户看的中文结论。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.10 节（EvalReport.Conclusions）。
//
// 硬要求：每条结论都必须由真实统计量推导。不允许出现与数据无关的固定套话 ——
// 一份看起来有结论、实际上和数字无关的报告，比没有报告更危险。

// 整体质量档位阈值（作用于归一化到 0~1 的总分）。
//
// 阈值选择理由：归一化后 0.85 大致对应「十个维度里九个接近满分」，
// 是「可以直接进训练集」的水平；0.7 对应「多数维度良好但有明显短板」，
// 属于需要挑选而非全量采用；0.5 以下意味着近半数维度不达标，
// 直接用于训练会引入系统性噪声。这些阈值是本项目的工程判断，
// 调整它们只需改这里，结论文案会自动跟随。
const (
	qualityExcellent = 0.85
	qualityGood      = 0.70
	qualityFair      = 0.50
)

// 裁判一致性阈值（作用于 [0,1] 的一致性分数）。
//
// 0.8 以上：裁判们排序基本一致，结论可信。
// 0.5 以下：分歧显著，任何基于平均分的结论都必须带保留意见。
const (
	agreementHigh = 0.80
	agreementLow  = 0.50
)

// 裁判系统性偏差阈值：某裁判的归一化均分与全体均分之差超过该值时点名。
// 取 0.1（归一化尺度上相当于量表区间的 10%），低于这个量级通常只是噪声。
const judgeBiasThreshold = 0.10

// BuildConclusions 生成中文结论列表。
//
// status 为 run 的状态；非 completed 时不产出质量结论，只说明进度 ——
// 未完成的评估不能给出「数据集质量优秀」这种判断。
func BuildConclusions(report model.EvalReport, notes AggregateNotes, status string) []string {
	if status != "" && status != "completed" {
		return []string{
			"评估尚未完成，当前状态：" + status + "。",
			"已评分 " + itoa(report.EvalRun.ScoredItems) + " / " + itoa(report.EvalRun.TotalItems) + " 条，",
			"此时不给出质量结论 —— 部分评分不足以代表整个数据集。",
		}
	}

	conclusions := []string{}

	// 1. 整体质量判断。
	conclusions = append(conclusions, overallConclusion(report, notes))

	// 2. 最弱维度 + 可操作建议。
	if weakest, ok := weakestDimension(report.Dimensions, notes); ok {
		// 只有一个维度时，最弱维度就是整体本身，「低于整体水平」是假的。
		// 同理，任何与整体持平的情况都不能声称它拉低了分数。
		//
		// 比较必须同口径：跨量表时原始分不可比（0~10 的 8 分与 0~100 的 50 分不能
		// 直接相减），因此优先用归一化分，并在文案里明确标出「归一化」，
		// 避免与前面的原始「均分」混成一句话里两个量纲的数字。
		comparison := ""
		if lowest, ok := notes.NormalizedDimensionScores[weakest.DimensionKey]; ok {
			if notes.HasNormalizedOverall && lowest < notes.NormalizedOverall {
				comparison = fmt.Sprintf("，归一化得分 %.2f，低于整体归一化水平 %.2f",
					lowest, notes.NormalizedOverall)
			}
		} else if weakest.Score < report.OverallScore {
			// 量表区间非法、无法归一化时，两边都只能用原始分，口径仍然一致。
			comparison = fmt.Sprintf("，低于整体水平 %.2f", report.OverallScore)
		}
		conclusions = append(conclusions, fmt.Sprintf(
			"最弱维度是「%s」（%s类），均分 %.2f%s。建议优先复查该维度对应的数据，%s。",
			weakest.Name, categoryLabel(weakest.Category), weakest.Score,
			comparison, dimensionAdvice(weakest)))
	}

	// 3. 最弱条目提示。
	if len(report.WeakestItems) > 0 {
		conclusions = append(conclusions, weakestItemsConclusion(report.WeakestItems))
	}

	// 4. 裁判一致性 —— 多 LLM 评估最重要的诚实性输出。
	conclusions = append(conclusions, agreementConclusion(report, notes)...)

	// 5. 裁判系统性偏差。
	conclusions = append(conclusions, judgeBiasConclusions(report, notes)...)

	// 6. 被跳过/降级的情况，必须让用户知道。
	conclusions = append(conclusions, degradationConclusions(notes)...)

	// 7. 一个分都没打出来的裁判，同样必须可见。
	if notice := silentJudgeConclusion(report, notes); notice != "" {
		conclusions = append(conclusions, notice)
	}

	// 8. 样本量提示。
	conclusions = append(conclusions, sampleConclusion(report))

	return conclusions
}

// overallConclusion 给出整体质量档位判断。
func overallConclusion(report model.EvalReport, notes AggregateNotes) string {
	// 优先用归一化分：不同维度的量表区间可能不同，直接比原始分没有可比性。
	//
	// 判据是 HasNormalizedOverall 而不是 > 0：归一化分恰为 0 是合法结果
	// （所有维度都打了各自量表的最低分），此时必须走档位分支得出
	// 「整体质量偏低」，而不是谎称「没有可用的量表区间」。
	if notes.HasNormalizedOverall {
		switch {
		case notes.NormalizedOverall >= qualityExcellent:
			return fmt.Sprintf("整体质量优秀（归一化得分 %.0f/100）。该数据集可以直接用于训练。",
				notes.NormalizedOverall*100)
		case notes.NormalizedOverall >= qualityGood:
			return fmt.Sprintf("整体质量良好（归一化得分 %.0f/100），但存在明显短板维度。建议按维度筛选后再使用。",
				notes.NormalizedOverall*100)
		case notes.NormalizedOverall >= qualityFair:
			return fmt.Sprintf("整体质量一般（归一化得分 %.0f/100）。近半数维度未达良好水平，直接用于训练会引入噪声，建议先按最弱维度做定向清洗。",
				notes.NormalizedOverall*100)
		default:
			return fmt.Sprintf("整体质量偏低（归一化得分 %.0f/100）。多数维度不达标，建议回到数据生成阶段重做，而非仅靠清洗修补。",
				notes.NormalizedOverall*100)
		}
	}

	// 没有可归一化的维度（量表区间非法或维度定义缺失）时，
	// 只能给原始加权分，并明确说明这是未归一化的数字。
	return fmt.Sprintf(
		"整体加权得分 %.2f。注意：本次评估没有可用的量表区间（scaleMin/scaleMax），无法归一化到统一尺度，该分数不能与其他数据集横向比较。",
		report.OverallScore)
}

// weakestDimension 找表现最差的维度。并列时取 key 较小者，保证结论稳定。
//
// 按**归一化分**排序：维度可以自定义量表区间（内置维度是 1~5，自定义维度
// 可能是 0~10 或 0~100），直接比原始分会把「0~10 打 8」判成比「0~100 打 50」
// 更差，点名实际表现最好的维度。归一化不可用（量表区间非法）的维度回退原始分。
func weakestDimension(stats []model.EvalDimensionStat, notes AggregateNotes) (model.EvalDimensionStat, bool) {
	if len(stats) == 0 {
		return model.EvalDimensionStat{}, false
	}

	lowest := func(stat model.EvalDimensionStat) float64 {
		if normalized, ok := notes.NormalizedDimensionScores[stat.DimensionKey]; ok {
			return normalized
		}
		return stat.Score
	}

	weakest := stats[0]
	for _, stat := range stats[1:] {
		if lowest(stat) < lowest(weakest) ||
			(lowest(stat) == lowest(weakest) && stat.DimensionKey < weakest.DimensionKey) {
			weakest = stat
		}
	}
	return weakest, true
}

// genericDimensionAdvice 未识别分类时的通用建议。
// 抽成常量供测试断言「该分类是否真的有专属建议」。
const genericDimensionAdvice = "建议抽查该维度得分最低的若干条数据，定位共性模式"

// dimensionAdvice 按维度分类给出可操作建议。
//
// 分类常量取自 catalog.go，不写字面量：内置 58 个维度分布在 7 个分类里，
// 手写字面量一旦与常量不同步，对应分类就会静默回退到通用建议。
func dimensionAdvice(stat model.EvalDimensionStat) string {
	switch stat.Category {
	case CategoryLongChain:
		return "重点看思维链是否跳步、是否有回溯与验证环节"
	case CategoryFaithfulness:
		return "重点看答案是否有原文支撑、有无编造事实"
	case CategoryInstruction:
		return "重点看是否严格遵循了问题里的格式、角色与约束要求"
	case CategoryDomainFit:
		return "重点看内容是否贴合该领域，有无答非所问或泛泛而谈"
	case CategoryAnswerQuality:
		return "重点看答案是否完整回答了问题、有无遗漏要点"
	case CategoryRobustness:
		return "重点看异常输入、边界条件下是否仍能稳定作答"
	case CategoryEfficiency:
		return "重点看推理是否冗长绕远、有无可合并的重复步骤"
	default:
		return genericDimensionAdvice
	}
}

// categoryLabel 把分类 key 翻译成中文。
func categoryLabel(category string) string {
	// 键必须与 catalog.go 的 Category* 常量一一对应。少一个键，结论里
	// 就会把分类名原样吐给用户（例如「domain_fit 类」），中文报告里
	// 混进英文 key 属于明显的交付缺陷。
	labels := map[string]string{
		CategoryLongChain:     "长链思考",
		CategoryFaithfulness:  "事实忠实",
		CategoryInstruction:   "指令遵循",
		CategoryDomainFit:     "领域贴合",
		CategoryAnswerQuality: "答案质量",
		CategoryRobustness:    "鲁棒性",
		CategoryEfficiency:    "推理效率",
	}
	if label, ok := labels[category]; ok {
		return label
	}
	if category == "" {
		return "未分类"
	}
	return category
}

// weakestItemsConclusion 描述最弱条目。
func weakestItemsConclusion(items []model.EvalItemScoreBrief) string {
	parts := make([]string, 0, len(items))
	for index, item := range items {
		if index >= 3 {
			break
		}
		// 面向用户展示时用 1-based 序号，与界面上的行号一致。
		parts = append(parts, fmt.Sprintf("第 %d 条（%.2f 分）", item.ItemIndex+1, item.Score))
	}
	return fmt.Sprintf("最低分数据：%s。共 %d 条进入最弱清单，建议优先人工复核。",
		strings.Join(parts, "、"), len(items))
}

// agreementConclusion 生成裁判一致性结论。
func agreementConclusion(report model.EvalReport, notes AggregateNotes) []string {
	conclusions := []string{}

	if report.JudgeAgreement == JudgeAgreementNotApplicable {
		// 一致性不适用时必须显式说明原因，不能留空或写 0。
		reason := "仅 1 个裁判参与了评分"
		if notes.AgreementPairs == 0 && len(notes.AgreementSkipped) > 0 {
			reason = notes.AgreementSkipped[0]
		}
		conclusions = append(conclusions, fmt.Sprintf(
			"裁判一致性不适用：%s。单个裁判的评分无法互相印证，本报告的结论缺少交叉验证。", reason))
		return conclusions
	}

	percent := report.JudgeAgreement * 100
	switch {
	case report.JudgeAgreement >= agreementHigh:
		conclusions = append(conclusions, fmt.Sprintf(
			"裁判一致性 %.0f/100，各裁判在数据排序上高度共识，本报告结论可信。", percent))
	case report.JudgeAgreement >= agreementLow:
		conclusions = append(conclusions, fmt.Sprintf(
			"裁判一致性 %.0f/100，存在一定分歧。结论大体可用，但争议条目的判定建议人工复核。", percent))
	default:
		// 这是最重要的警示，措辞必须明确。
		conclusions = append(conclusions, fmt.Sprintf(
			"⚠️ 裁判一致性仅 %.0f/100，各裁判分歧较大，结论可信度受限。平均分掩盖了模型之间的判断差异，建议增加裁判数量或对争议条目人工复核后再采信本报告。",
			percent))
	}

	// 说明有几对裁判参与了比较，让用户能判断这个数字的统计基础。
	if notes.AgreementPairs > 0 {
		conclusions = append(conclusions, fmt.Sprintf(
			"一致性由 %d 对裁判的两两秩相关平均得出（秩相关衡量排序共识，不受裁判打分尺度差异影响）。",
			notes.AgreementPairs))
	}
	for _, skipped := range notes.AgreementSkipped {
		conclusions = append(conclusions, "一致性计算跳过："+skipped+"。")
	}
	return conclusions
}

// judgeBiasConclusions 识别并点名打分系统性偏高/偏低的裁判。
//
// 这是暴露「裁判本身有偏差」的关键输出：若某个模型对任何数据都给高分，
// 它会把整体均分抬高，让数据集看起来比实际更好。
func judgeBiasConclusions(report model.EvalReport, notes AggregateNotes) []string {
	if len(report.Judges) < 2 {
		return nil
	}

	// 只比较有归一化均分的裁判；量表区间缺失的裁判无法参与横向比较。
	type scored struct {
		label string
		value float64
	}
	values := make([]scored, 0, len(report.Judges))
	var sum float64
	for _, judge := range report.Judges {
		normalized, ok := notes.NormalizedJudgeMeans[judge.ProviderID]
		if !ok {
			continue
		}
		values = append(values, scored{label: judgeLabelFor(judge), value: normalized})
		sum += normalized
	}
	if len(values) < 2 {
		return nil
	}
	average := sum / float64(len(values))

	conclusions := []string{}
	for _, item := range values {
		delta := item.value - average
		if math.Abs(delta) < judgeBiasThreshold {
			continue
		}
		direction := "偏高"
		if delta < 0 {
			direction = "偏低"
		}
		conclusions = append(conclusions, fmt.Sprintf(
			"裁判「%s」打分系统性%s：其归一化均分 %.2f，全体裁判均分 %.2f，相差 %.2f。该裁判的评分可能拉%s整体分数，建议复核其评判标准。",
			item.label, direction, item.value, average, math.Abs(delta), directionWord(direction)))
	}
	return conclusions
}

// directionWord 用于拼接「拉高/拉低」。
func directionWord(direction string) string {
	if direction == "偏高" {
		return "高"
	}
	return "低"
}

// judgeLabelFor 为 EvalJudgeStat 生成人类可读名称。
func judgeLabelFor(judge model.EvalJudgeStat) string {
	if judge.ProviderName != "" && judge.Model != "" {
		return judge.ProviderName + "/" + judge.Model
	}
	if judge.ProviderName != "" {
		return judge.ProviderName
	}
	if judge.Model != "" {
		return judge.Model
	}
	return "provider#" + itoa64(judge.ProviderID)
}

// degradationConclusions 把聚合过程中被跳过的情况翻译成用户可读的说明。
//
// 这些情况必须出现在结论里：用户配置了某个维度却发现它没影响总分，
// 如果没有一行说明，用户只会以为系统算错了。
func degradationConclusions(notes AggregateNotes) []string {
	conclusions := []string{}

	if len(notes.ZeroWeightDimensions) > 0 {
		conclusions = append(conclusions, fmt.Sprintf(
			"以下维度权重为 0 或负值，已被排除在加权总分之外（其分数仍单独展示）：%s。若这不是本意，请调整维度权重。",
			strings.Join(notes.ZeroWeightDimensions, "、")))
	}
	if len(notes.UnweightedDimensions) > 0 {
		conclusions = append(conclusions, fmt.Sprintf(
			"以下维度有分数但找不到维度定义，未计入总分：%s。通常是维度被删除或 key 不匹配。",
			strings.Join(notes.UnweightedDimensions, "、")))
	}
	if notes.FailedScores > 0 {
		conclusions = append(conclusions, fmt.Sprintf(
			"有 %d 条打分未成功（状态非 scored），已排除在全部统计之外 —— 失败记录的分值为 0，计入会凭空拉低均分。",
			notes.FailedScores))
	}
	if len(notes.ExcludedJudges) > 0 {
		conclusions = append(conclusions, fmt.Sprintf(
			"以下裁判已被剔除，未参与本次评分：%s。剔除生成该数据集的模型是为了避免自评偏差；若这是误判，请调整裁判配置后重跑。",
			strings.Join(notes.ExcludedJudges, "、")))
	}
	return conclusions
}

// silentJudgeConclusion 点名「一条分都没打出来」的裁判。
//
// 这类裁判平均分是 0，但它既不是「打了 0 分」也不是「没参加」。如果不提示，
// 用户会拿一个 0 分去和别的裁判对比，得出「这个模型很严格」的错误结论。
//
// 已被剔除的裁判要排除在外：他们本来就不该打分，上面第 6 步已经说明过原因，
// 再说一句「调用失败」是自相矛盾的。
func silentJudgeConclusion(report model.EvalReport, notes AggregateNotes) string {
	excluded := map[int64]struct{}{}
	for _, providerID := range notes.ExcludedJudgeIDs {
		excluded[providerID] = struct{}{}
	}

	silent := []string{}
	for _, judge := range report.Judges {
		if judge.SampleCount != 0 {
			continue
		}
		if _, isExcluded := excluded[judge.ProviderID]; isExcluded {
			continue
		}
		silent = append(silent, judgeLabelFor(judge))
	}
	if len(silent) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"以下裁判没有任何有效打分，其均分 0 不代表打分严格，而是调用失败：%s。请查看该裁判的失败原因后重试。",
		strings.Join(silent, "、"))
}

// sampleConclusion 提示样本量，并警示小样本下的结论不稳。
func sampleConclusion(report model.EvalReport) string {
	if report.SampleCount == 0 {
		return "本次评估没有任何成功评分的条目，报告中的全部统计量均为空值，不具参考意义。"
	}
	if report.SampleCount < 10 {
		return fmt.Sprintf(
			"本次仅 %d 条数据参与评分，样本量偏小，均分与一致性指标波动较大，建议扩大抽样范围后再下结论。",
			report.SampleCount)
	}
	return fmt.Sprintf("本次共 %d 条数据参与评分。", report.SampleCount)
}

// BuildSummaries 把报告压平成可持久化的汇总行（表 eval_summaries）。
//
// scope 取值与 0011_eval_core.sql 的注释一致：overall / judge / dimension / item。
// 这些行让前端不必每次请求都重算全量聚合。
func BuildSummaries(report model.EvalReport) []model.EvalSummary {
	summaries := make([]model.EvalSummary, 0, len(report.Judges)+len(report.Dimensions)+2)

	summaries = append(summaries, model.EvalSummary{
		EvalRunID:   report.EvalRun.ID,
		Scope:       "overall",
		RefKey:      "",
		Score:       report.OverallScore,
		SampleCount: report.SampleCount,
		Detail: map[string]any{
			"judgeAgreement": report.JudgeAgreement,
			"datasetName":    report.DatasetName,
		},
	})

	for _, judge := range report.Judges {
		summaries = append(summaries, model.EvalSummary{
			EvalRunID:   report.EvalRun.ID,
			Scope:       "judge",
			RefKey:      itoa64(judge.ProviderID),
			Score:       judge.Score,
			SampleCount: judge.SampleCount,
			Detail: map[string]any{
				"providerName": judge.ProviderName,
				"model":        judge.Model,
			},
		})
	}

	for _, dimension := range report.Dimensions {
		summaries = append(summaries, model.EvalSummary{
			EvalRunID:   report.EvalRun.ID,
			Scope:       "dimension",
			RefKey:      dimension.DimensionKey,
			Score:       dimension.Score,
			SampleCount: dimension.SampleCount,
			Detail: map[string]any{
				"name":     dimension.Name,
				"category": dimension.Category,
				"stdDev":   dimension.StdDev,
				"min":      dimension.Min,
				"max":      dimension.Max,
			},
		})
	}

	for _, item := range report.WeakestItems {
		summaries = append(summaries, model.EvalSummary{
			EvalRunID:   report.EvalRun.ID,
			Scope:       "item",
			RefKey:      itoa64(item.QuestionID),
			Score:       item.Score,
			SampleCount: 1,
			Detail: map[string]any{
				"itemIndex": item.ItemIndex,
			},
		})
	}

	// 按 scope + refKey 排序，让写入顺序稳定、便于比对。
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].Scope != summaries[j].Scope {
			return summaries[i].Scope < summaries[j].Scope
		}
		return summaries[i].RefKey < summaries[j].RefKey
	})
	return summaries
}

// itoa 十进制整数转字符串。
func itoa(value int) string {
	return strconv.Itoa(value)
}

// itoa64 十进制 int64 转字符串。
func itoa64(value int64) string {
	return strconv.FormatInt(value, 10)
}
