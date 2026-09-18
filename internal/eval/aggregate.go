package eval

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// 本文件实现 L10 的聚合统计：把 L9 写入的逐条逐维度打分，汇总成
// 报告所需的全部统计量（model.EvalReport）。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.10 节。
//
// 设计原则：这里全部是纯函数，不碰 DB、不调 LLM。
// 统计口径一旦出错，报告会给出错误结论，因此每个口径都必须能被单测锁定。

// JudgeAgreementNotApplicable 是「裁判一致性不适用」的哨兵值。
//
// 一致性是「多个裁判之间」的指标，只有 1 个裁判时它在数学上没有定义。
// 此时如果返回 0，前端会把它读成「完全不一致」—— 那是截然相反的结论，
// 会让用户误以为整份评估不可信。因此返回一个落在合法区间 [0,1] 之外的
// 哨兵值，强制调用方显式区分「不适用」与「不一致」两种情况。
const JudgeAgreementNotApplicable = -1.0

// WeakestItemLimit 报告里「最弱条目」最多列出多少条。
const WeakestItemLimit = 10

// AggregateInput 聚合所需的全部输入。
// 都是纯数据，便于单测直接构造，不需要 DB。
type AggregateInput struct {
	Run         model.EvalRun
	DatasetName string
	Items       []model.EvalItem
	Scores      []model.EvalItemScore
	Judges      []model.EvalRunJudge
	Dimensions  []model.EvalDimension
}

// AggregateNotes 记录聚合过程中被跳过、降级或需要向用户解释的情况。
//
// 这些信息不能塞进 model.EvalReport（那是冻结契约），但结论生成必须用到它们：
// 「哪些维度因为权重为 0 被排除在总分之外」「一致性为什么算不出来」
// 都是用户有权知道的事，静默吞掉等于伪造一份看起来完整的报告。
type AggregateNotes struct {
	// NormalizedOverall 把加权总分按各维度自身的 (ScaleMin, ScaleMax) 归一化到 0~1。
	// 结论的分数档位判断用它，因为不同维度的量纲可能不同，直接比原始分没有意义。
	NormalizedOverall float64

	// NormalizedJudgeMeans 每个裁判的均分，同样归一化到 0~1。
	// 用于识别「某个裁判系统性打分偏高/偏低」。
	NormalizedJudgeMeans map[int64]float64

	// ZeroWeightDimensions 权重 <= 0 因而被排除在加权总分之外的维度 key。
	ZeroWeightDimensions []string

	// UnweightedDimensions 加权总分里没有任何可用分数（该维度一条分都没有）的维度 key。
	UnweightedDimensions []string

	// FailedScores 状态不是 scored 的打分条数。这些分数不参与任何统计，
	// 否则一条失败的 0 分会把均分拉低。
	FailedScores int

	// AgreementPairs 实际参与一致性计算的两两裁判组合数。
	AgreementPairs int

	// AgreementSkipped 被跳过的一致性比较及原因。
	AgreementSkipped []string

	// ExcludedJudges 被 L7 剔除、未参与评分的裁判及其原因。
	// 必须在结论里告知用户，否则「为什么少了一个模型」无从查起。
	ExcludedJudges []string
}

// Aggregate 把逐条逐维度打分汇总成报告统计量。
//
// 返回的第二值是聚合过程中的降级说明，结论生成需要它。
func Aggregate(in AggregateInput) (model.EvalReport, AggregateNotes) {
	notes := AggregateNotes{
		NormalizedJudgeMeans: map[int64]float64{},
		ZeroWeightDimensions: []string{},
		UnweightedDimensions: []string{},
		AgreementSkipped:     []string{},
		ExcludedJudges:       []string{},
	}

	report := model.EvalReport{
		EvalRun:        in.Run,
		DatasetName:    in.DatasetName,
		JudgeAgreement: JudgeAgreementNotApplicable,
		Judges:         []model.EvalJudgeStat{},
		Dimensions:     []model.EvalDimensionStat{},
		WeakestItems:   []model.EvalItemScoreBrief{},
		Conclusions:    []string{},
		GeneratedAt:    time.Now().UTC(),
	}

	dimensions := dimensionIndex(in.Dimensions)
	items := itemIndex(in.Items)

	// 只统计真正打成功的分数：一条 status=failed 的记录 score 是 0，
	// 混进平均值会凭空拉低结论。
	valid := make([]model.EvalItemScore, 0, len(in.Scores))
	for _, score := range in.Scores {
		if score.Status != "" && score.Status != "scored" {
			notes.FailedScores++
			continue
		}
		valid = append(valid, score)
	}

	// 每个维度的原始均分（跨全部裁判、全部条目）。
	dimensionScores := groupByDimension(valid)
	report.Dimensions = buildDimensionStats(dimensionScores, dimensions)

	// 加权总分：先算每个维度的均分，再按维度权重加权。
	// 权重 <= 0 的维度直接排除，并在 notes 里记名 —— 用户配了个 0 权重
	// 却发现分数没变，必须能查到原因。
	// 失败或未参与的裁判必须让用户看得见 —— L7 会剔除生成者模型的自评，
	// 若报告里不提一句，用户会以为所有登记的裁判都参与了。
	for _, judge := range in.Judges {
		if !judge.Excluded {
			continue
		}
		reason := judge.ExcludeReason
		if reason == "" {
			reason = "未说明原因"
		}
		notes.ExcludedJudges = append(notes.ExcludedJudges,
			judgeLabel(judge)+"（"+reason+"）")
	}

	var weightedSum, weightTotal float64
	for _, stat := range report.Dimensions {
		dimension, ok := dimensions[stat.DimensionKey]
		if !ok {
			// 维度定义已不存在（被删或 key 写错）。分数还在，但不能加权。
			notes.UnweightedDimensions = append(notes.UnweightedDimensions, stat.DimensionKey)
			continue
		}
		if dimension.Weight <= 0 {
			notes.ZeroWeightDimensions = append(notes.ZeroWeightDimensions, stat.DimensionKey)
			continue
		}
		weightedSum += stat.Score * dimension.Weight
		weightTotal += dimension.Weight
	}
	sort.Strings(notes.ZeroWeightDimensions)
	sort.Strings(notes.UnweightedDimensions)

	if weightTotal > 0 {
		report.OverallScore = weightedSum / weightTotal
	}
	notes.NormalizedOverall, _ = normalizedMean(report.Dimensions, dimensions)

	// 每个裁判的统计。传入裁判名单，让「一条分都没打」的裁判也出现在报告里。
	report.Judges = buildJudgeStats(valid, in.Judges, items, dimensions)
	for _, judgeStat := range report.Judges {
		if normalized, ok := normalizedMean(judgeStat.Dimensions, dimensions); ok {
			notes.NormalizedJudgeMeans[judgeStat.ProviderID] = normalized
		}
	}

	// 裁判一致性：只对「至少 2 个裁判」才有定义。
	report.JudgeAgreement, notes.AgreementPairs, notes.AgreementSkipped =
		judgeAgreement(valid, in.Judges)

	// 最弱条目。
	report.WeakestItems = weakestItems(valid, items)
	report.SampleCount = countScoredItems(valid)

	return report, notes
}

// dimensionIndex 按 key 建立维度索引。
func dimensionIndex(dimensions []model.EvalDimension) map[string]model.EvalDimension {
	index := make(map[string]model.EvalDimension, len(dimensions))
	for _, dimension := range dimensions {
		index[dimension.Key] = dimension
	}
	return index
}

// itemIndex 按 id 建立条目索引，用于把打分还原成「第几条数据」。
func itemIndex(items []model.EvalItem) map[int64]model.EvalItem {
	index := make(map[int64]model.EvalItem, len(items))
	for _, item := range items {
		index[item.ID] = item
	}
	return index
}

// groupByDimension 按维度 key 归集分数。
func groupByDimension(scores []model.EvalItemScore) map[string][]float64 {
	grouped := make(map[string][]float64, 64)
	for _, score := range scores {
		grouped[score.DimensionKey] = append(grouped[score.DimensionKey], score.Score)
	}
	return grouped
}

// buildDimensionStats 生成逐维度统计，按 key 升序保证输出稳定。
func buildDimensionStats(
	grouped map[string][]float64,
	dimensions map[string]model.EvalDimension,
) []model.EvalDimensionStat {
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	stats := make([]model.EvalDimensionStat, 0, len(keys))
	for _, key := range keys {
		values := grouped[key]
		stat := model.EvalDimensionStat{
			DimensionKey: key,
			Score:        mean(values),
			SampleCount:  len(values),
			StdDev:       stdDev(values),
			Min:          minOf(values),
			Max:          maxOf(values),
		}
		if dimension, ok := dimensions[key]; ok {
			stat.Name = dimension.Name
			stat.Category = dimension.Category
		} else {
			// 维度定义缺失时至少把 key 当名字返回，不要让前端显示空白。
			stat.Name = key
		}
		stats = append(stats, stat)
	}
	return stats
}

// buildJudgeStats 生成逐裁判统计。
//
// 以 in.Judges（eval_run_judges 登记名单）为基准，而不是只遍历有分数的裁判：
// 某个裁判全程调用失败、一条分都没打时，他必须仍然出现在报告里（SampleCount=0），
// 否则用户看到的是一个「所有裁判都正常」的假象。
// 名单里没有但确实打了分的裁判（例如 run 登记不完整）也会补上。
func buildJudgeStats(
	scores []model.EvalItemScore,
	judges []model.EvalRunJudge,
	items map[int64]model.EvalItem,
	dimensions map[string]model.EvalDimension,
) []model.EvalJudgeStat {
	byJudge := map[int64][]model.EvalItemScore{}
	for _, score := range scores {
		byJudge[score.JudgeProviderID] = append(byJudge[score.JudgeProviderID], score)
	}

	meta := map[int64]model.EvalRunJudge{}
	providerIDs := make([]int64, 0, len(judges)+len(byJudge))
	for _, judge := range judges {
		if _, exists := meta[judge.ProviderID]; exists {
			continue
		}
		meta[judge.ProviderID] = judge
		providerIDs = append(providerIDs, judge.ProviderID)
	}
	for providerID := range byJudge {
		if _, exists := meta[providerID]; exists {
			continue
		}
		providerIDs = append(providerIDs, providerID)
	}
	sort.Slice(providerIDs, func(i, j int) bool { return providerIDs[i] < providerIDs[j] })

	stats := make([]model.EvalJudgeStat, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		judgeScores := byJudge[providerID]

		stat := model.EvalJudgeStat{
			ProviderID: providerID,
			Dimensions: []model.EvalDimensionStat{},
			ItemScores: []model.EvalItemScoreBrief{},
		}
		if judge, ok := meta[providerID]; ok {
			stat.ProviderName = judge.ProviderName
			stat.Model = judge.Model
		}
		stat.SampleCount = len(judgeScores)

		values := make([]float64, 0, len(judgeScores))
		for _, score := range judgeScores {
			values = append(values, score.Score)
		}
		stat.Score = mean(values)
		stat.Dimensions = buildDimensionStats(groupByDimension(judgeScores), dimensions)
		stat.ItemScores = buildItemBriefs(judgeScores, items)

		stats = append(stats, stat)
	}
	return stats
}

// buildItemBriefs 把一个裁判在各条目上的分数聚合成逐条均分，按 itemIndex 升序。
func buildItemBriefs(scores []model.EvalItemScore, items map[int64]model.EvalItem) []model.EvalItemScoreBrief {
	byItem := map[int64][]float64{}
	for _, score := range scores {
		byItem[score.EvalItemID] = append(byItem[score.EvalItemID], score.Score)
	}

	briefs := make([]model.EvalItemScoreBrief, 0, len(byItem))
	for itemID, values := range byItem {
		brief := model.EvalItemScoreBrief{Score: mean(values)}
		if item, ok := items[itemID]; ok {
			brief.QuestionID = item.QuestionID
			brief.ItemIndex = item.ItemIndex
		} else {
			// 条目记录缺失时用 eval_item_id 兜底，至少让用户能定位到是哪一行。
			brief.QuestionID = itemID
		}
		briefs = append(briefs, brief)
	}
	sort.Slice(briefs, func(i, j int) bool {
		if briefs[i].ItemIndex != briefs[j].ItemIndex {
			return briefs[i].ItemIndex < briefs[j].ItemIndex
		}
		return briefs[i].QuestionID < briefs[j].QuestionID
	})
	return briefs
}

// weakestItems 找出得分最低的若干条数据。
//
// 这里刻意用「不加权的算术平均」而不是加权平均：这张表是用来做排查的
// ——「哪几条数据最可疑」。若按维度权重加权，一个高权重维度的良好表现
// 会掩盖某个低权重维度上的崩坏，恰恰把最该被人工看一眼的那条藏起来。
func weakestItems(scores []model.EvalItemScore, items map[int64]model.EvalItem) []model.EvalItemScoreBrief {
	byItem := map[int64][]float64{}
	for _, score := range scores {
		byItem[score.EvalItemID] = append(byItem[score.EvalItemID], score.Score)
	}

	briefs := make([]model.EvalItemScoreBrief, 0, len(byItem))
	for itemID, values := range byItem {
		brief := model.EvalItemScoreBrief{Score: mean(values)}
		if item, ok := items[itemID]; ok {
			brief.QuestionID = item.QuestionID
			brief.ItemIndex = item.ItemIndex
		} else {
			brief.QuestionID = itemID
		}
		briefs = append(briefs, brief)
	}

	sort.Slice(briefs, func(i, j int) bool {
		if briefs[i].Score != briefs[j].Score {
			return briefs[i].Score < briefs[j].Score
		}
		// 同分时按条目顺序稳定排序，避免两次请求给出不同顺序。
		if briefs[i].ItemIndex != briefs[j].ItemIndex {
			return briefs[i].ItemIndex < briefs[j].ItemIndex
		}
		return briefs[i].QuestionID < briefs[j].QuestionID
	})

	if len(briefs) > WeakestItemLimit {
		briefs = briefs[:WeakestItemLimit]
	}
	return briefs
}

// countScoredItems 统计有分数的不同条目数。
func countScoredItems(scores []model.EvalItemScore) int {
	seen := map[int64]struct{}{}
	for _, score := range scores {
		seen[score.EvalItemID] = struct{}{}
	}
	return len(seen)
}

// judgeAgreement 计算多裁判一致性。
//
// 算法选择：两两 **Spearman 秩相关**，再线性映射到 [0,1]（(rho+1)/2），
// 最后对所有有效裁判对取算术平均。
//
// 为什么用秩相关而不是「逐条分数平均绝对差」：
// 一致性要回答的是「裁判们是否在排序上达成共识」，而不是「他们的绝对分是否相等」。
// 平均绝对差会把「系统性偏移」也算成分歧 —— 例如裁判 B 对每条数据都比 A 高 2 分，
// 但两人的相对排序完全一致。那属于「裁判打分尺度不同」，是另一回事，
// 本包用 NormalizedJudgeMeans 单独识别并点名（见 report.go 的裁判偏差结论）。
// 若把两者混进同一个数字，用户就再也分不清「模型之间真的吵架了」
// 还是「某个模型手松」。
//
// 返回：(一致性, 参与比较的裁判对数, 被跳过的原因列表)
func judgeAgreement(scores []model.EvalItemScore, judges []model.EvalRunJudge) (float64, int, []string) {
	skipped := []string{}

	// 每个裁判在每个条目上的均分，用于构造可比序列。
	// 一个裁判可能对同一条目打多个维度，这里先按条目聚合成均分，
	// 这样不同裁判比较的是同一把尺子（条目级得分），而不是维度级别的原始分。
	byJudge := map[int64]map[int64]float64{}
	byJudgeCount := map[int64]map[int64]int{}
	for _, score := range scores {
		if byJudge[score.JudgeProviderID] == nil {
			byJudge[score.JudgeProviderID] = map[int64]float64{}
			byJudgeCount[score.JudgeProviderID] = map[int64]int{}
		}
		byJudge[score.JudgeProviderID][score.EvalItemID] += score.Score
		byJudgeCount[score.JudgeProviderID][score.EvalItemID]++
	}
	for providerID, itemScores := range byJudge {
		for itemID, total := range itemScores {
			itemScores[itemID] = total / float64(byJudgeCount[providerID][itemID])
		}
	}

	providerIDs := make([]int64, 0, len(byJudge))
	for providerID := range byJudge {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Slice(providerIDs, func(i, j int) bool { return providerIDs[i] < providerIDs[j] })

	nameOf := map[int64]string{}
	for _, judge := range judges {
		nameOf[judge.ProviderID] = judgeLabel(judge)
	}

	// 只有一个裁判时，一致性没有定义 —— 返回哨兵值而不是 0。
	if len(providerIDs) < 2 {
		if len(providerIDs) == 1 {
			skipped = append(skipped,
				"仅 1 个裁判参与了评分，裁判一致性无法计算（一致性是裁判之间的指标）")
		}
		return JudgeAgreementNotApplicable, 0, skipped
	}

	total := 0.0
	pairs := 0
	for i := 0; i < len(providerIDs); i++ {
		for j := i + 1; j < len(providerIDs); j++ {
			leftID, rightID := providerIDs[i], providerIDs[j]
			itemIDs := sharedItemIDs(byJudge[leftID], byJudge[rightID])

			label := nameOf[leftID] + " ↔ " + nameOf[rightID]
			if len(itemIDs) < 2 {
				skipped = append(skipped, label+"：共同评分的条目不足 2 条，秩相关无定义")
				continue
			}

			left := make([]float64, 0, len(itemIDs))
			right := make([]float64, 0, len(itemIDs))
			for _, itemID := range itemIDs {
				left = append(left, byJudge[leftID][itemID])
				right = append(right, byJudge[rightID][itemID])
			}

			rho, ok := spearman(left, right)
			if !ok {
				skipped = append(skipped, label+"：某一侧打分无变化，秩相关分母为 0，无法计算")
				continue
			}
			total += (rho + 1) / 2
			pairs++
		}
	}

	if pairs == 0 {
		return JudgeAgreementNotApplicable, 0, skipped
	}
	return total / float64(pairs), pairs, skipped
}

// sharedItemIDs 返回两个裁判都评过的条目 id，升序。
func sharedItemIDs(left, right map[int64]float64) []int64 {
	ids := make([]int64, 0, len(left))
	for itemID := range left {
		if _, ok := right[itemID]; ok {
			ids = append(ids, itemID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// judgeLabel 给裁判起一个人类可读的名字，用于结论文案。
func judgeLabel(judge model.EvalRunJudge) string {
	if judge.ProviderName != "" && judge.Model != "" {
		return judge.ProviderName + "/" + judge.Model
	}
	if judge.ProviderName != "" {
		return judge.ProviderName
	}
	if judge.Model != "" {
		return judge.Model
	}
	return "provider#" + strconv.FormatInt(judge.ProviderID, 10)
}

// spearman 计算两组等长序列的 Spearman 秩相关系数。
//
// 返回 (rho, ok)；ok=false 表示无定义 —— 序列长度不足 2，
// 或任一侧的秩完全无变化（此时相关系数分母为 0）。
// 无定义时**不编造一个值**：调用方必须知道这个数字算不出来。
func spearman(left, right []float64) (float64, bool) {
	if len(left) != len(right) || len(left) < 2 {
		return 0, false
	}

	leftRanks := ranks(left)
	rightRanks := ranks(right)
	if isConstant(leftRanks) || isConstant(rightRanks) {
		return 0, false
	}

	leftMean := mean(leftRanks)
	rightMean := mean(rightRanks)

	var sumLeft, sumRight, sumBoth float64
	for index := range leftRanks {
		deltaLeft := leftRanks[index] - leftMean
		deltaRight := rightRanks[index] - rightMean
		sumLeft += deltaLeft * deltaLeft
		sumRight += deltaRight * deltaRight
		sumBoth += deltaLeft * deltaRight
	}
	if sumLeft == 0 || sumRight == 0 {
		return 0, false
	}
	return sumBoth / math.Sqrt(sumLeft*sumRight), true
}

// ranks 返回平均秩（并列取平均），保证 Spearman 在存在并列分数时仍然正确。
func ranks(values []float64) []float64 {
	order := make([]int, len(values))
	for index := range order {
		order[index] = index
	}
	sort.Slice(order, func(i, j int) bool { return values[order[i]] < values[order[j]] })

	result := make([]float64, len(values))
	for start := 0; start < len(order); {
		end := start
		for end+1 < len(order) && values[order[end+1]] == values[order[start]] {
			end++
		}
		// 并列组的平均秩：秩从 1 开始，因此是 (start+1 + end+1) / 2。
		average := float64(start+1+end+1) / 2
		for index := start; index <= end; index++ {
			result[order[index]] = average
		}
		start = end + 1
	}
	return result
}

// isConstant 判断序列是否所有元素相同。
func isConstant(values []float64) bool {
	for index := 1; index < len(values); index++ {
		if values[index] != values[0] {
			return false
		}
	}
	return true
}

// normalizeScore 把原始分按维度自身的量表区间映射到 0~1。
// 量表区间非法（max <= min）时返回 ok=false，不猜一个区间出来。
func normalizeScore(score float64, dimension model.EvalDimension) (float64, bool) {
	span := float64(dimension.ScaleMax - dimension.ScaleMin)
	if span <= 0 {
		return 0, false
	}
	normalized := (score - float64(dimension.ScaleMin)) / span
	if normalized < 0 {
		return 0, true
	}
	if normalized > 1 {
		return 1, true
	}
	return normalized, true
}

// normalizedMean 把一组维度统计先按各维度自身的量表区间归一化，再按维度权重加权平均。
//
// 不能先算「跨维度原始均分」再拿某一个维度的量表去解释它：不同维度的量表
// 区间可以完全不同（内置维度是 1~5，用户自定义维度可能是 0~10 或 0~100），
// 跨维度原始均分本身没有统一量纲，用一个维度的区间去归一化它得到的数字
// 没有意义，会让「某裁判打分偏高/偏低」的判断整体失真。
//
// 返回 ok=false 表示没有任何可用于归一化的维度（量表区间非法或权重全为 0）。
func normalizedMean(
	stats []model.EvalDimensionStat, dimensions map[string]model.EvalDimension,
) (float64, bool) {
	var sum, weightTotal float64
	for _, stat := range stats {
		dimension, ok := dimensions[stat.DimensionKey]
		if !ok || dimension.Weight <= 0 {
			continue
		}
		normalized, ok := normalizeScore(stat.Score, dimension)
		if !ok {
			continue
		}
		sum += normalized * dimension.Weight
		weightTotal += dimension.Weight
	}
	if weightTotal <= 0 {
		return 0, false
	}
	return sum / weightTotal, true
}

// mean 算术平均。空输入返回 0（调用方靠 SampleCount 区分「0 分」与「没有样本」）。
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

// stdDev 总体标准差（除以 n，而非 n-1）。
//
// 选择总体而非样本标准差：这里统计的是「这批被评估数据本身的离散程度」，
// 不是用样本去推断更大总体的参数。n=1 时返回 0 而不是 NaN —— 单条数据
// 没有离散可言，返回 NaN 会让前端渲染出 "NaN"。
func stdDev(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	average := mean(values)
	var sum float64
	for _, value := range values {
		delta := value - average
		sum += delta * delta
	}
	return math.Sqrt(sum / float64(len(values)))
}

// minOf 最小值。空输入返回 0。
func minOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

// maxOf 最大值。空输入返回 0。
func maxOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}
