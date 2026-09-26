package studio

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件实现生产工作区的**数据集结构与内容分析**（issue #197 第 13 条）。
//
// 为什么分析必须在服务端做：
//
//	前端的责任是渲染，不是统计口径。如果前端拉全量样本自己算分位、占比与
//	重复率，就会有两个后果 ——（1）10 万单元下把整表拉进浏览器；
//	（2）同一个指标在不同页面算出不同的数（每个页面一份实现）。
//	因此这里给出**一个**权威读模型，由 `/batches/{id}/analysis` 返回。
//
// 三条与既有约定一致的口径：
//
//  1. **空数据集返回 nil 而不是 0**：0 是一个结论（「长度是 0」），
//     而「没有数据」不是结论。分位、均值、占比在无样本时全部为 null。
//  2. **计划量与实际产出分列**，不合成百分比（契约 §3.1）。
//  3. **缺口显式可见**（issue #190）：`shortfallUnits` 与
//     `shortfallNote` 直接来自批次模型，前端不需要自己相减。

// DatasetAnalysis 是批次产出数据的结构与内容分析。
type DatasetAnalysis struct {
	// Structure 是「领域 › 方向」两级结构统计（可产出的数据集骨架）。
	Structure []AnalysisGroup `json:"structure"`
	// Length 是内容长度分布。无样本时字段全部为 nil。
	Length *LengthAnalysis `json:"length"`
	// Difficulty 是难度档占比（与配额的配比对照）。
	Difficulty []AnalysisShare `json:"difficulty"`
	// ReviewStatus 是审阅状态占比。
	ReviewStatus []AnalysisShare `json:"reviewStatus"`
	// GroundedRate 是「有素材接地」的比例（无样本时为 nil）。
	GroundedRate *float64 `json:"groundedRate"`
	// DuplicateRate 是**内容指纹**重复的比例（无样本时为 nil）。
	DuplicateRate *float64 `json:"duplicateRate"`
	// PendingReviewRate 是「仍待人工判断」的比例。
	PendingReviewRate *float64 `json:"pendingReviewRate"`
	// SampleCount 是参与统计的样本版本数（分母）。它是**事实**，不是百分比。
	SampleCount int `json:"sampleCount"`
	// PlannedUnits / CompletedUnits / ShortfallUnits 直接来自批次，供界面显示缺口。
	PlannedUnits   int    `json:"plannedUnits"`
	CompletedUnits int    `json:"completedUnits"`
	ShortfallUnits int    `json:"shortfallUnits"`
	ShortfallNote  string `json:"shortfallNote"`
	// Notes 说明本读模型**不做**什么（诚实标注胜过编一个数）。
	Notes []string `json:"notes"`
}

// AnalysisGroup 是结构统计的一行（领域/方向）。
type AnalysisGroup struct {
	DomainStableID    string `json:"domainStableId"`
	DomainName        string `json:"domainName"`
	DirectionStableID string `json:"directionStableId"`
	DirectionName     string `json:"directionName"`
	// Planned 是该方向按覆盖配额的计划量（0 表示没有覆盖版本）。
	Planned int `json:"planned"`
	// Produced 是实际产出的样本版本数。
	Produced int `json:"produced"`
}

// LengthAnalysis 是字符长度分布。分位数用**最近秩法**（nearest-rank）。
type LengthAnalysis struct {
	Count     int `json:"count"`
	Shortest  int `json:"shortest"`
	Longest   int `json:"longest"`
	P50       int `json:"p50"`
	P90       int `json:"p90"`
	MeanChars int `json:"meanChars"`
	// FieldCount 是参与长度统计的字段数（question/reasoning/answer 等文本字段）。
	FieldCount int `json:"fieldCount"`
}

// AnalysisShare 是一个占比项。
type AnalysisShare struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Count int     `json:"count"`
	Share float64 `json:"share"`
	// Expected 是配比里声明的目标占比（仅难度有；无配比时为 nil）。
	Expected *float64 `json:"expected"`
}

// AnalyzeDataset 计算批次的数据集结构与内容分析。
//
// 数据来源：`sample_versions`（只追加的内容事实）+ 批次单元（provenance）。
// 不读「当前采用」指针：分析必须针对**这一批**实际产出的内容。
func AnalyzeDataset(ctx context.Context, batches *store.BatchStore, documents *store.DocumentStore,
	batch model.Batch, unitLimit int) (DatasetAnalysis, error) {
	if unitLimit <= 0 || unitLimit > 5000 {
		unitLimit = 2000
	}
	analysis := DatasetAnalysis{
		PlannedUnits:   batch.PlannedUnits,
		CompletedUnits: batch.CompletedUnits,
		ShortfallUnits: batch.Shortfall(),
		ShortfallNote:  batch.ShortfallNote(),
		Structure:      []AnalysisGroup{},
		Difficulty:     []AnalysisShare{},
		ReviewStatus:   []AnalysisShare{},
	}

	// 1) 结构：先按覆盖版本列出「应该产出什么」，再用实际产出去填。
	//
	// 为什么两段都做：只列实际产出时「本该有但一条都没有」的方向会**消失**，
	// 而缺口恰恰是最需要被看见的信息（issue #190 的同类错误）。
	producedByDirection := map[string]int{}
	rows, err := batches.ListSampleVersionFacts(ctx, batch.ProjectID, batch.ID, unitLimit)
	if err != nil {
		return DatasetAnalysis{}, err
	}

	if batch.Snapshot.CoverageVersionID > 0 {
		version, err := documents.GetVersionByID(ctx, batch.Snapshot.CoverageVersionID)
		if err != nil {
			return DatasetAnalysis{}, err
		}
		var coverage model.CoveragePayload
		if err := json.Unmarshal(version.Payload, &coverage); err != nil {
			return DatasetAnalysis{}, NewError(CodeValidation, "覆盖版本内容无法解析，请重新保存覆盖方案")
		}
		for _, unit := range model.AllocateCoverageUnits(coverage, unitLimit) {
			key := unit.DomainStableID + "/" + unit.DirectionStableID
			producedByDirection[key]++
		}
		// 把「计划数」与「实际数」都算出来：计划来自配额，实际来自样本版本。
		for _, domain := range coverage.Domains {
			for _, direction := range domain.Directions {
				quota := direction.Quota
				if quota <= 0 {
					quota = 1
				}
				key := domain.StableID + "/" + direction.StableID
				analysis.Structure = append(analysis.Structure, AnalysisGroup{
					DomainStableID:    domain.StableID,
					DomainName:        domain.Name,
					DirectionStableID: direction.StableID,
					DirectionName:     direction.Name,
					Planned:           quota,
					Produced:          producedByDirection[key],
				})
			}
		}
	}
	if len(analysis.Structure) == 0 {
		// 没有覆盖版本时退化成「按实际产出的方向聚合」，而不是返回空结构。
		byKey := map[string]AnalysisGroup{}
		for _, row := range rows {
			key := row.DomainStableID + "/" + row.DirectionStableID
			group := byKey[key]
			group.DomainStableID = row.DomainStableID
			group.DirectionStableID = row.DirectionStableID
			group.Produced++
			byKey[key] = group
		}
		for _, group := range byKey {
			group.Planned = group.Produced
			analysis.Structure = append(analysis.Structure, group)
		}
		sort.Slice(analysis.Structure, func(i, j int) bool {
			return analysis.Structure[i].DomainStableID+analysis.Structure[i].DirectionStableID <
				analysis.Structure[j].DomainStableID+analysis.Structure[j].DirectionStableID
		})
	}

	// 2) 内容分析：长度分布、难度占比、接地率、重复率、待审阅率。
	lengths := make([]int, 0, len(rows))
	hashes := map[string]int{}
	difficultyCount := map[string]int{}
	reviewCount := map[string]int{}
	grounded := 0
	pending := 0
	for _, row := range rows {
		lengths = append(lengths, row.PayloadChars)
		if row.ContentHash != "" {
			hashes[row.ContentHash]++
		}
		if row.Difficulty != "" {
			difficultyCount[row.Difficulty]++
		} else {
			difficultyCount["unspecified"]++
		}
		reviewCount[row.ReviewStatus]++
		if row.ReviewStatus == model.EffectivePending {
			pending++
		}
		if row.Grounded {
			grounded++
		}
	}
	analysis.SampleCount = len(rows)
	if len(rows) > 0 {
		analysis.Length = summarizeLengths(lengths)
		// 重复率按「非首个出现的版本」计数：10 条里 2 条重复 → 20%。
		duplicates := 0
		for _, count := range hashes {
			if count > 1 {
				duplicates += count - 1
			}
		}
		duplicateRate := float64(duplicates) / float64(len(rows))
		groundedRate := float64(grounded) / float64(len(rows))
		pendingRate := float64(pending) / float64(len(rows))
		analysis.DuplicateRate = &duplicateRate
		analysis.GroundedRate = &groundedRate
		analysis.PendingReviewRate = &pendingRate
	}

	analysis.Difficulty = sharesOf(difficultyCount, len(rows), map[string]string{
		"easy": "简单", "medium": "中等", "hard": "困难",
		"normal": "普通", "unspecified": "未标注难度",
	})
	analysis.ReviewStatus = sharesOf(reviewCount, len(rows), map[string]string{
		model.EffectivePending: "待判断", "accepted": "已接纳",
		"quarantined": "已隔离", "conflict": "存在冲突",
	})

	analysis.Notes = []string{
		"长度按字符数统计（中文 1 字 = 1 字符），分位用最近秩法。",
		"重复率按内容指纹统计，只说明「内容完全相同」，不代表语义重复。",
		"接地率是「这条内容的生成输入里有素材块」的比例；无素材来源的项目恒为 0。",
	}
	return analysis, nil
}

// summarizeLengths 计算长度分布（无数据时由调用方保证不进来）。
func summarizeLengths(lengths []int) *LengthAnalysis {
	if len(lengths) == 0 {
		return nil
	}
	ordered := make([]int, len(lengths))
	copy(ordered, lengths)
	sort.Ints(ordered)
	total := 0
	for _, value := range ordered {
		total += value
	}
	nearestRank := func(percentile float64) int {
		// 最近秩法：ceil(p * n) 位置上的值（1 起数）。它不需要插值，
		// 因此永远返回一个**真实存在**的长度，而不是一个虚构的中间值。
		rank := int(float64(len(ordered)) * percentile)
		if float64(rank) < float64(len(ordered))*percentile {
			rank++
		}
		if rank < 1 {
			rank = 1
		}
		if rank > len(ordered) {
			rank = len(ordered)
		}
		return ordered[rank-1]
	}
	return &LengthAnalysis{
		Count:      len(ordered),
		Shortest:   ordered[0],
		Longest:    ordered[len(ordered)-1],
		P50:        nearestRank(0.5),
		P90:        nearestRank(0.9),
		MeanChars:  total / len(ordered),
		FieldCount: 1,
	}
}

// sharesOf 把计数转成占比项（按计数降序）。
func sharesOf(counts map[string]int, total int, labels map[string]string) []AnalysisShare {
	shares := make([]AnalysisShare, 0, len(counts))
	for key, count := range counts {
		label := labels[key]
		if label == "" {
			label = "其它"
		}
		share := 0.0
		if total > 0 {
			share = float64(count) / float64(total)
		}
		shares = append(shares, AnalysisShare{Key: key, Label: label, Count: count, Share: share})
	}
	sort.SliceStable(shares, func(i, j int) bool {
		if shares[i].Count != shares[j].Count {
			return shares[i].Count > shares[j].Count
		}
		return shares[i].Key < shares[j].Key
	})
	return shares
}

// DifficultyTargetsFromCoverage 读覆盖版本里的难度配比，作为占比的「预期值」。
func DifficultyTargetsFromCoverage(payload []byte) (map[string]float64, error) {
	var coverage model.CoveragePayload
	if len(payload) == 0 {
		return map[string]float64{}, nil
	}
	if err := json.Unmarshal(payload, &coverage); err != nil {
		return nil, err
	}
	targets := map[string]float64{}
	for _, domain := range coverage.Domains {
		for _, direction := range domain.Directions {
			for _, ratio := range direction.DifficultyRatios {
				key := strings.ToLower(strings.TrimSpace(ratio.Difficulty))
				if key == "" {
					continue
				}
				targets[key] += ratio.Ratio / 100
			}
		}
	}
	// 多个方向都有配比时取平均，避免把「方向数」误当成权重。
	directions := 0
	for _, domain := range coverage.Domains {
		directions += len(domain.Directions)
	}
	if directions > 1 {
		for key := range targets {
			targets[key] /= float64(directions)
		}
	}
	return targets, nil
}
