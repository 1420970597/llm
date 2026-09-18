package llm

import "github.com/1420970597/llm/internal/model"

// 难度分层常量。difficulty 字符串与 difficulty_score 必须一致。
const (
	DifficultyEasy   = "easy"
	DifficultyMedium = "medium"
	DifficultyHard   = "hard"
)

// DifficultyScoreOf 返回难度对应的数值分（1/2/3）。
func DifficultyScoreOf(level string) int {
	switch level {
	case DifficultyEasy:
		return 1
	case DifficultyHard:
		return 3
	default:
		return 2
	}
}

// DifficultyAssigner 为第 index/total 个问题分配难度。
// L3 在 difficulty_planner.go 的 init() 中覆盖它，以支持用户给定的 difficultyMix。
// 默认实现按简单/中等/困难 = 3:4:3 循环分配，保证任何调用方都能拿到分层结果。
var DifficultyAssigner = func(index, total int) string {
	if total <= 0 {
		return DifficultyMedium
	}
	bucket := (index * 10) / total
	switch {
	case bucket < 3:
		return DifficultyEasy
	case bucket < 7:
		return DifficultyMedium
	default:
		return DifficultyHard
	}
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
	for _, level := range []string{DifficultyEasy, DifficultyMedium, DifficultyHard} {
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

// DifficultyFromMix 按配比与序号挑选难度，保证每档至少出现一次（当数量足够时）。
func DifficultyFromMix(mix map[string]float64, index, total int) string {
	if len(mix) == 0 || total <= 0 {
		return DifficultyAssigner(index, total)
	}
	// 先按配比切分区间，再按序号落桶。
	order := []string{DifficultyEasy, DifficultyMedium, DifficultyHard}
	cumulative := 0.0
	position := float64(index) / float64(total)
	for _, level := range order {
		weight, ok := mix[level]
		if !ok {
			continue
		}
		cumulative += weight
		if position < cumulative {
			return level
		}
	}
	return DifficultyHard
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
