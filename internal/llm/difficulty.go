package llm

import (
	"sort"

	"github.com/1420970597/llm/internal/model"
)

// 难度分层常量。difficulty 字符串与 difficulty_score 必须一致。
const (
	DifficultyEasy   = "easy"
	DifficultyMedium = "medium"
	DifficultyHard   = "hard"
)

// difficultyOrder 是按「由易到难」排列的档位顺序。
//
// 配额展开（difficultyPlan）与提示词渲染（renderQuota）都用它，保证同一配额下的
// 分配结果可重现，且这两处不会各自维护一份顺序而与配额分配脱节。
var difficultyOrder = []string{DifficultyEasy, DifficultyMedium, DifficultyHard}

// difficultyAssignerMix 是 DifficultyAssigner 使用的默认配比：简单/中等/困难 = 3:4:3。
//
// 只用于 v1 生成路径（question_generator.go）的回退分配。v2 路径
// （question_generator_v2.go）使用用户给定的配比，未指定时回退到
// defaultDifficultyMix（0.3/0.5/0.2）。两份默认值刻意分开：它们服务于两条不同的
// 生成路径，合并其中一份不应静默改变另一条路径的历史分布。
var difficultyAssignerMix = map[string]float64{
	DifficultyEasy:   0.3,
	DifficultyMedium: 0.4,
	DifficultyHard:   0.3,
}

// difficultyRank 返回档位的难度序（easy=0, medium=1, hard=2）。
//
// 排序与数值分共用这一个序，避免出现「排序按一个顺序、打分按另一个顺序」的漂移。
// 无法识别的档位按 medium 处理。
func difficultyRank(level string) int {
	switch level {
	case DifficultyEasy:
		return 0
	case DifficultyHard:
		return 2
	default:
		return 1
	}
}

// DifficultyScoreOf 返回难度对应的数值分（1/2/3）。
func DifficultyScoreOf(level string) int {
	return difficultyRank(level) + 1
}

// allocateDifficultyQuota 按配比把 total 个名额精确分配到各难度档。
//
// 这是难度分层的唯一配额算法：DifficultyAssigner、DifficultyFromMix 与
// AllocateDifficultyMix（v2 生成路径）全部走这里，因此三条入口不会算出不同分布。
//
// 规则：
//  1. 只有配比中权重 > 0 的档位参与分配。权重为 0 或缺失的档位恒为 0 —— 用户
//     可以显式要求「不要困难题」，此时不强行补足。
//  2. 名额总数 >= 参与档位数时，每一档至少 1 个。这是本函数的核心保证：小 total
//     下不再丢档（历史缺陷见 issue #10 / #11，生产数据里 total=2 时困难档恒为 0）。
//  3. 名额总数 < 参与档位数时，槽位不足以覆盖所有档位，按「难度降序」保留前
//     total 个档位：total=2 → 困难 + 中等，total=1 → 困难。理由是低难度样本在
//     正常生成中本就最容易产出，而困难档缺失会让困难能力无法训练 —— 「困难档在
//     total 较小时完全消失」正是 issue #10 记录的生产缺陷，因此小 total 下优先保住
//     高难度档，而不是优先保住高权重档。
//  4. 余数用最大余数法补足（小数部分大者优先，相同则难度高者优先），保证各档之和
//     恰好等于 total，不出现多算或少算。
func allocateDifficultyQuota(total int, normalizedMix map[string]float64) map[string]int {
	quota := map[string]int{DifficultyEasy: 0, DifficultyMedium: 0, DifficultyHard: 0}
	if total <= 0 || len(normalizedMix) == 0 {
		return quota
	}

	levels := make([]string, 0, len(difficultyOrder))
	for _, level := range difficultyOrder {
		if weight, ok := normalizedMix[level]; ok && weight > 0 {
			levels = append(levels, level)
		}
	}
	if len(levels) == 0 {
		return quota
	}

	if total < len(levels) {
		// 规则 3：槽位不足，按难度降序保留前 total 个档位。
		ranked := append([]string(nil), levels...)
		sort.SliceStable(ranked, func(i, j int) bool {
			return difficultyRank(ranked[i]) > difficultyRank(ranked[j])
		})
		for _, level := range ranked[:total] {
			quota[level] = 1
		}
		return quota
	}

	type remainder struct {
		level string
		frac  float64
	}
	remainders := make([]remainder, 0, len(levels))
	assigned := 0
	for _, level := range levels {
		exact := normalizedMix[level] * float64(total)
		// 加极小量抵消浮点误差（0.3*10 在 float64 下为 2.9999999999999996）。
		floored := int(exact + 1e-9)
		if floored < 0 {
			floored = 0
		}
		quota[level] = floored
		assigned += floored
		remainders = append(remainders, remainder{level: level, frac: exact - float64(floored)})
	}

	// 规则 4：余数按小数部分从大到小补 1；小数部分相同时难度高者优先，
	// 使结果既贴近配比又确定可重现。
	sort.SliceStable(remainders, func(i, j int) bool {
		if remainders[i].frac != remainders[j].frac {
			return remainders[i].frac > remainders[j].frac
		}
		return difficultyRank(remainders[i].level) > difficultyRank(remainders[j].level)
	})
	for index := 0; assigned < total; index++ {
		quota[remainders[index%len(remainders)].level]++
		assigned++
	}

	// 规则 2：把不足 1 个的档位补到 1 个，并从当前名额最多的档位扣减。
	// 由 sum(quota) == total >= len(levels) 可证：只要还有欠账，名额最多的档位
	// 必然 >= 2，因此扣减不会把任何档位压到 0 以下，且循环必然终止。
	deficit := 0
	for _, level := range levels {
		if quota[level] < 1 {
			deficit += 1 - quota[level]
			quota[level] = 1
		}
	}
	for ; deficit > 0; deficit-- {
		quota[largestQuotaLevel(levels, quota, normalizedMix)]--
	}
	return quota
}

// largestQuotaLevel 返回当前名额最多的档位，供补齐保底名额时扣减。
//
// 名额相同时先比配比权重、再比难度序，保证结果可重现。
func largestQuotaLevel(levels []string, quota map[string]int, mix map[string]float64) string {
	best := levels[0]
	for _, level := range levels[1:] {
		switch {
		case quota[level] > quota[best]:
			best = level
		case quota[level] == quota[best] && mix[level] > mix[best]:
			best = level
		case quota[level] == quota[best] && mix[level] == mix[best] && difficultyRank(level) > difficultyRank(best):
			best = level
		}
	}
	return best
}

// DifficultyAssigner 为第 index/total 个问题分配难度（v1 生成路径的回退分配）。
//
// 实现按 difficultyAssignerMix（3:4:3）做配额分配，保证：
//   - total >= 3 时三档齐全，每档至少 1 条；
//   - total < 3 时按难度降序保留档位（total=2 → 困难 + 中等，total=1 → 困难），
//     确保小 total 下困难档不会消失（历史缺陷见 issue #10）。
//
// 该变量可被测试替换以注入固定分布；v2 路径的难度由用户配比经
// AllocateDifficultyMix 计算，不经过本变量。
var DifficultyAssigner = func(index, total int) string {
	if total <= 0 {
		return DifficultyMedium
	}
	return difficultyLevelAt(allocateDifficultyQuota(total, difficultyAssignerMix), index, total)
}

// difficultyLevelAt 从配额中取出第 index 个档位。
//
// 展开顺序固定为 difficultyOrder，与 difficultyPlan 完全一致，因此
// DifficultyFromMix 与 v2 生成路径对同一配比、同一 index 给出同一个档位。
// index 越界时收敛到首尾，不 panic。
func difficultyLevelAt(allocation map[string]int, index, total int) string {
	plan := difficultyPlan(allocation, total)
	if len(plan) == 0 {
		return DifficultyMedium
	}
	position := index
	if position < 0 {
		position = 0
	}
	if position >= len(plan) {
		position = len(plan) - 1
	}
	return plan[position]
}

// AssignDifficulty 计算难度字符串与数值分。
func AssignDifficulty(index, total int) (string, int) {
	level := DifficultyAssigner(index, total)
	if level != DifficultyEasy && level != DifficultyMedium && level != DifficultyHard {
		level = DifficultyMedium
	}
	return level, DifficultyScoreOf(level)
}

// NormalizeDifficultyMix 校验并归一化用户给定的难度配比。
// 返回空 map 表示未指定，调用方应回退到默认循环分配。
func NormalizeDifficultyMix(mix map[string]float64) map[string]float64 {
	if len(mix) == 0 {
		return nil
	}
	normalized := map[string]float64{}
	total := 0.0
	for _, level := range difficultyOrder {
		value, ok := mix[level]
		if !ok || value < 0 {
			continue
		}
		normalized[level] = value
		total += value
	}
	if total <= 0 {
		return nil
	}
	for level := range normalized {
		normalized[level] = normalized[level] / total
	}
	return normalized
}

// DifficultyFromMix 按配比与序号挑选难度。
//
// 「名额足够」的定义是 total >= 配比中权重 > 0 的档位数：此时每档至少出现一次。
// 名额不足时按难度降序保留档位，规则与 allocateDifficultyQuota 一致（见其注释规则 3）。
// mix 为空或无效时回退到 DifficultyAssigner。
//
// 该函数与 v2 生成路径共用同一套配额算法与展开顺序，因此同一配比下两个入口对同一个
// index 给出同一个档位（由 TestDifficultyFromMixMatchesGenerationPlan 锁定）。
func DifficultyFromMix(mix map[string]float64, index, total int) string {
	if total <= 0 {
		return DifficultyAssigner(index, total)
	}
	normalized := NormalizeDifficultyMix(mix)
	if len(normalized) == 0 {
		return DifficultyAssigner(index, total)
	}
	return difficultyLevelAt(allocateDifficultyQuota(total, normalized), index, total)
}

// ChainStepFromDomain 把长链标准步骤压缩成问题生成可用的上下文摘要。
func ChainStepFromDomain(steps []model.ChainStep) string {
	if len(steps) == 0 {
		return ""
	}
	builder := ""
	for _, step := range steps {
		if step.Title == "" {
			continue
		}
		if builder != "" {
			builder += " → "
		}
		builder += step.Title
	}
	return builder
}
