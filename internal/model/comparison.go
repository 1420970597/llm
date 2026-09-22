package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 本文件定义同基准试制比较（Issue #160 T18）。
//
// 契约：docs/plans/atelier-implementation.md §3（P06）、
// docs/plans/atelier-api-contract.md §2.5。
//
// 本文件的核心主张：**比较必须先固定前提，否则差异主要来自输入而非方案**。
// 任意两个批次的问题集合、覆盖切片、量表与抽样都可能不同，把它们的平均分
// 相减得到的「提升 12%」看起来很有说服力，但它是错的 —— 而那种错误
// 会导致把资源投在一个并未变好的方案上。

// 比较口径。
const (
	// ComparisonMetricPaired 逐题配对：两侧输入**相同**（同一份问题集合），
	// 因此可以按题比较「同一题在两个方案下的输出」。
	ComparisonMetricPaired = "paired"
	// ComparisonMetricCoverage 覆盖生成本身：两侧输入**不同**（各自生成覆盖），
	// 因此**不能**声称逐题配对 —— 只能比较覆盖分布与总量。
	ComparisonMetricCoverage = "coverage"
)

// ComparisonBaseline 是一份冻结的比较前提。
type ComparisonBaseline struct {
	ID            int64          `json:"id"`
	ProjectID     int64          `json:"projectId"`
	InputRef      string         `json:"inputRef"`
	CoverageSlice map[string]any `json:"coverageSlice"`
	SamplingSeed  int64          `json:"samplingSeed"`
	Rubric        RubricSpec     `json:"rubric"`
	Judges        []JudgeSpec    `json:"judges"`
	Metric        string         `json:"metric"`
	LeftBatchID   *int64         `json:"leftBatchId,omitempty"`
	RightBatchID  *int64         `json:"rightBatchId,omitempty"`
	Name          string         `json:"name"`
	CreatedBy     *int64         `json:"createdBy,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
}

// ValidateComparisonBaseline 校验一份比较前提。
//
// 三条硬要求，每条都对应一个会产出错误结论的具体错法：
//
//  1. **两侧都要有批次**：只给一侧无法比较，而「先建 baseline 再补另一侧」
//     会让中间状态下有人误以为可以看报告。
//  2. **两侧不能相同**：与自己比较得到的差异恒为 0，用户会以为「方案没差别」。
//  3. **配对口径必须有输入约束**：声明 paired 却不给 inputRef（固定输入问题
//     版本）时，两侧输入可能不同 —— 那正是「拿两个任意批次相减」的错。
//     宁可拒绝创建，也不产出一个无法解释的「可比」标签。
func ValidateComparisonBaseline(baseline ComparisonBaseline) error {
	var errs FieldErrors

	if baseline.LeftBatchID == nil || baseline.RightBatchID == nil {
		errs = append(errs, FieldError{Field: "batches",
			Message: "比较必须同时指定两侧批次"})
	} else if *baseline.LeftBatchID == *baseline.RightBatchID {
		errs = append(errs, FieldError{Field: "batches",
			Message: "两侧不能是同一个批次（与自己比较的差异恒为 0）"})
	}
	switch baseline.Metric {
	case ComparisonMetricPaired:
		if strings.TrimSpace(baseline.InputRef) == "" {
			// 这是本函数最重要的一条：声明「逐题配对」却不固定输入，
			// 会让报告的「配对完成数」变成一个无法解释的数字。
			errs = append(errs, FieldError{Field: "inputRef",
				Message: "逐题配对比较必须固定输入问题版本（否则两侧输入可能不同，" +
					"差异主要来自输入而不是方案）"})
		}
	case ComparisonMetricCoverage:
		// 覆盖比较的输入本来就不同，因此不要求 inputRef；
		// 但报告里必须写明「不是逐题配对」（见 ComparisonReport 的 Comparability）。
		if len(baseline.CoverageSlice) == 0 {
			errs = append(errs, FieldError{Field: "coverageSlice",
				Message: "覆盖比较必须指定比较的切片范围"})
		}
	default:
		errs = append(errs, FieldError{Field: "metric",
			Message: "比较口径只能是 paired（逐题配对）或 coverage（覆盖生成）"})
	}
	if err := baseline.Rubric.Validate(); err != nil {
		errs = append(errs, FieldError{Field: "rubric",
			Message: "量表必须可用：" + err.Error()})
	}
	if len(baseline.Judges) == 0 {
		errs = append(errs, FieldError{Field: "judges",
			Message: "比较必须指定裁判（两侧用同一组裁判才可比）"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// PairedObservation 是同一题在某维度上、两方案各一次评分。
type PairedObservation struct {
	// PairKey 是配对的键（单元键/问题稳定 ID）。两侧必须用**同一个键空间**，
	// 否则「配对完成数」会统计出不相交的集合。
	PairKey string
	// Dimension 是该观测所属的维度。
	//
	// 初版把维度归属留给调用方「第一个维度」，那在多维度量表下会把所有
	// 维度的分数都算进第一个维度 —— 报告看起来正常，但每个维度的均值都是错的。
	// 因此维度必须随观测一起来。
	Dimension string
	// LeftScore/RightScore 为 nil 表示该侧缺分（不是 0）。
	LeftScore  *float64
	RightScore *float64
}

// ComparisonReport 是比较报告。
//
// 刻意**不包含**显著性、p 值或「最佳方案」断言：
//
//	一个试制的样本量通常远不足以支撑显著性结论，而给出一个数字会让用户
//	把它当成结论。报告只陈述事实（配对数、缺失数、维度差异与覆盖），
//	由人结合成本与风险决定采用哪一侧（T18 验收项明确禁止无依据断言）。
type ComparisonReport struct {
	BaselineID int64  `json:"baselineId"`
	Metric     string `json:"metric"`
	// Comparability 说明这份报告**可以**支持什么结论、不可以支持什么。
	// 它是报告的一部分而不是界面的文案：换一个界面仍然要说同一件事。
	Comparability Comparability `json:"comparability"`

	// 配对完成数是配对口径的**唯一有效样本量**（不是两侧批次的总数）。
	PairedCount int `json:"pairedCount"`
	// LeftOnlyCount/RightOnlyCount 是只在一侧有分或只在一侧存在的题数。
	// 它们必须单独列出：把「只有一侧有」的题算进配对会让差异失真。
	LeftOnlyCount  int `json:"leftOnlyCount"`
	RightOnlyCount int `json:"rightOnlyCount"`

	Dimensions []PairedDimensionDiff `json:"dimensions"`
	// Risks 是必须与结论一起看的事实（缺失、覆盖损失、未知成本）。
	Risks []string `json:"risks"`
	// Costs 是两侧成本（实际/未知分开；未知不得记 0）。
	Costs ComparisonCosts `json:"costs"`
}

// Comparability 说明「这份比较能支持什么结论」。
type Comparability struct {
	// Comparable 表示两侧是否真的可比（口径 + 前提是否满足）。
	Comparable bool `json:"comparable"`
	// Label 是给用户看的一句话（例如「同基准逐题配对，可比较同一题的方案差异」）。
	Label string `json:"label"`
	// Disclaimers 是必须同时显示的限定（例如「样本量不足以支持显著性结论」）。
	Disclaimers []string `json:"disclaimers"`
}

// PairedDimensionDiff 是一个维度的配对差异。
type PairedDimensionDiff struct {
	Dimension string `json:"dimension"`
	// LeftMean/RightMean 只统计**配对且两侧都有分**的观测。
	LeftMean  float64 `json:"leftMean"`
	RightMean float64 `json:"rightMean"`
	// Delta 是右 - 左（归一化后）。正数表示右侧更好。
	Delta float64 `json:"delta"`
	// Pairs 是参与该维度计算的配对数（分母，必须与 PairedCount 一起看）。
	Pairs int `json:"pairs"`
	// Missing 是两侧至少一侧缺分的观测数（不参与均值，也不补 0）。
	Missing int `json:"missing"`
}

// ComparisonCosts 是两侧成本。
type ComparisonCosts struct {
	LeftActualMinor     int64  `json:"leftActualMinor"`
	LeftUncertainMinor  int64  `json:"leftUncertainMinor"`
	RightActualMinor    int64  `json:"rightActualMinor"`
	RightUncertainMinor int64  `json:"rightUncertainMinor"`
	Currency            string `json:"currency"`
}

// BuildComparisonReport 由配对观测构建报告。
//
// 四条规则（每条都对应一个会误导用户的错法）：
//
//  1. **配对完成数才是有效样本量**：不是两侧批次的总数，也不是「筛选总数」。
//  2. **只有一侧有的观测不计入均值**：把它算成 0 会凭空制造差异。
//  3. **缺分不补 0**（沿用 §2.6）。
//  4. **必须给出限定**：样本量、口径、缺失都写进 Disclaimers，
//     否则用户会把一个小样本差异当成结论。
func BuildComparisonReport(baseline ComparisonBaseline, observations []PairedObservation) ComparisonReport {
	report := ComparisonReport{BaselineID: baseline.ID, Metric: baseline.Metric}

	dimensions := map[string]*PairedDimensionDiff{}
	for _, dimension := range baseline.Rubric.Dimensions {
		dimensions[dimension.Key] = &PairedDimensionDiff{Dimension: dimension.Key}
	}

	leftSum, leftCount := map[string]float64{}, map[string]int{}
	rightSum, rightCount := map[string]float64{}, map[string]int{}

	for _, observation := range observations {
		dimensionKey := observation.Dimension
		diff := dimensions[dimensionKey]
		if diff == nil {
			// 观测引用了量表里没有的维度：跳过而不是归到某个维度 ——
			// 归错会让某个维度的均值包含不属于它的分数。
			continue
		}
		switch {
		case observation.LeftScore != nil && observation.RightScore != nil:
			// 配对完成：两侧都有分。
			leftSum[dimensionKey] += *observation.LeftScore
			leftCount[dimensionKey]++
			rightSum[dimensionKey] += *observation.RightScore
			rightCount[dimensionKey]++
			report.PairedCount++
		case observation.LeftScore != nil:
			report.LeftOnlyCount++
			diff.Missing++
		case observation.RightScore != nil:
			report.RightOnlyCount++
			diff.Missing++
		default:
			// 两侧都缺分：既不算配对也不算「只有一侧」——
			// 它对差异没有任何信息量，但必须计入缺失以便解释样本量。
			diff.Missing++
		}
	}

	// 统计量的方向说明：Delta = 右 - 左，正数表示右侧更好。
	// 方向写反是这类报告最危险的错误（用户会据此选错方案），因此固定语义并测试。
	for key, diff := range dimensions {
		diff.Pairs = leftCount[key]
		if leftCount[key] > 0 {
			diff.LeftMean = leftSum[key] / float64(leftCount[key])
		}
		if rightCount[key] > 0 {
			diff.RightMean = rightSum[key] / float64(rightCount[key])
		}
		diff.Delta = diff.RightMean - diff.LeftMean
		report.Dimensions = append(report.Dimensions, *diff)
	}
	sort.SliceStable(report.Dimensions, func(i, j int) bool {
		return report.Dimensions[i].Dimension < report.Dimensions[j].Dimension
	})

	report.Comparability = comparabilityFor(baseline, report)
	report.Risks = comparisonRisks(report)
	return report
}

// comparabilityFor 判定「可比」标签与必须同时显示的限定。
//
// 关键：**口径决定了能说什么**。
//   - paired 且已固定输入 → 可以说「同一题在两方案下的差异」；
//   - coverage → 只能说「覆盖分布与总量的差异」，**不得**声称逐题配对；
//   - 任何情况下都不作显著性/最佳方案断言（样本量通常不足）。
func comparabilityFor(baseline ComparisonBaseline, report ComparisonReport) Comparability {
	result := Comparability{Comparable: true}
	switch baseline.Metric {
	case ComparisonMetricPaired:
		result.Label = "同基准逐题配对：比较的是同一题在两个方案下的输出差异"
		result.Disclaimers = append(result.Disclaimers,
			"两侧输入已固定为同一份问题版本，因此差异来自方案而非输入")
	case ComparisonMetricCoverage:
		result.Label = "覆盖生成比较：两侧输入不同，比较的是覆盖分布与总量"
		result.Disclaimers = append(result.Disclaimers,
			"两侧输入不同，因此**不能**声明逐题配对；本题差异不代表方案优劣")
	default:
		// 口径未知：拒绝「可比」标签而不是猜一个。
		return Comparability{Comparable: false,
			Label:       "无法判定可比性：比较口径缺失",
			Disclaimers: []string{"请重新创建比较基准并指定口径（配对的或覆盖的）"}}
	}

	if report.PairedCount == 0 {
		return Comparability{Comparable: false,
			Label: "不可比较：没有任何配对完成的观测",
			Disclaimers: []string{
				"配对完成数为 0 时比较没有有效样本；请检查两侧是否有共同的问题集合与共同裁判",
			}}
	}
	if report.PairedCount < 20 {
		result.Disclaimers = append(result.Disclaimers, fmt.Sprintf(
			"配对完成数仅 %d，样本量不足以支持「显著更好」这类结论；请把它当作方向性参考",
			report.PairedCount))
	}
	if report.LeftOnlyCount+report.RightOnlyCount > 0 {
		result.Disclaimers = append(result.Disclaimers, fmt.Sprintf(
			"有 %d 题只在一侧有评分、%d 题只在另一侧有评分，它们不计入差异",
			report.LeftOnlyCount, report.RightOnlyCount))
	}
	// 明确禁止的解释，写进响应而不是留给界面自由发挥。
	result.Disclaimers = append(result.Disclaimers,
		"报告不提供显著性检验与「最佳方案」断言：请结合成本、风险与覆盖损失自行决定")
	return result
}

// comparisonRisks 汇总必须与结论一起看的事实。
func comparisonRisks(report ComparisonReport) []string {
	risks := []string{}
	for _, dimension := range report.Dimensions {
		if dimension.Missing > 0 {
			risks = append(risks, fmt.Sprintf(
				"维度 %s 有 %d 个未配对观测（缺分不计为 0 分）",
				dimension.Dimension, dimension.Missing))
		}
	}
	if report.PairedCount == 0 {
		risks = append(risks, "没有配对完成的观测，报告不能支持任何方案对比结论")
	}
	return risks
}

// ComparisonCostRisks 给出成本维度的风险说明。
//
// 抽成独立函数是因为报告构建是**纯函数**（读不到数据库），
// 而成本来自 store。把它独立出来使「未知费用必须出现在结论旁边」这条
// 不依赖调用顺序：谁组装报告谁就得调用它并追加到 Risks。
func ComparisonCostRisks(costs ComparisonCosts) []string {
	risks := []string{}
	if costs.LeftUncertainMinor > 0 || costs.RightUncertainMinor > 0 {
		risks = append(risks, fmt.Sprintf(
			"存在未知费用（左 %d %s、右 %d %s）：超时或断连的调用可能已产生费用，"+
				"成本对比因此不完整", costs.LeftUncertainMinor, costs.Currency,
			costs.RightUncertainMinor, costs.Currency))
	}
	if costs.LeftActualMinor == 0 && costs.RightActualMinor == 0 &&
		costs.LeftUncertainMinor == 0 && costs.RightUncertainMinor == 0 {
		risks = append(risks, "两侧都还没有已结算费用：成本对比暂不可用（不代表免费）")
	}
	return risks
}

// ValidateAdoption 校验一次采用决定。
//
// 「采用」只更新项目采用指针并记录依据，**不自动运行或发布**
// （T18 验收项）：因此这里要求理由与依据，而不接受「直接跑扩量」的语义。
func ValidateAdoption(side, reason string, report ComparisonReport) error {
	var errs FieldErrors
	if side != "left" && side != "right" {
		errs = append(errs, FieldError{Field: "adoptedSide", Message: "只能采用 left 或 right 一侧"})
	}
	if strings.TrimSpace(reason) == "" {
		errs = append(errs, FieldError{Field: "reason",
			Message: "采用方案必须写明依据（没有依据的决定无法在以后复核）"})
	}
	if !report.Comparability.Comparable {
		errs = append(errs, FieldError{Field: "baseline",
			Message: "该比较不可比（" + report.Comparability.Label + "），不能作为采用依据"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ComparisonCapabilities 派生比较页的能力位。
//
// `CanAdopt` 只在**可比**时为真（对齐「基准不一致时拒绝可比标签」）：
// 采用一个不可比的比较，等于用一个无法解释的差异做决定。
func ComparisonCapabilities(role string, comparable bool, adopted bool) Capabilities {
	isOwner := role == ProjectRoleOwner
	return Capabilities{
		CanEdit:     isOwner && comparable,
		CanRun:      isOwner && comparable && !adopted,
		CanReview:   isOwner || role == ProjectRoleReviewer,
		CanPublish:  false,
		CanDownload: true,
	}
}
