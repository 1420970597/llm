package cleaning

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// 本文件实现 L12「分步清洗」的扫描引擎。
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.12 节。
//
// 命名隔离（父代理 2026-09-18 裁决，见 tasks/L12.md）：包 internal/cleaning 由
// L11 与 L12 共用，标识符严格划分——L11 独占 Match / MatchKeywords /
// BuiltinKeywords / Snippet / EvaluateRules；L12 独占本文件的 ScannerMatch /
// Matcher / ScanTarget / ScanResult / RuleSpec。
//
// 本文件刻意不引用 L11 的任何符号，因此可以注入 fake matcher 独立单测，
// 不受 L11 并行开发进度影响。L11 的命中结果由 apps/worker/job_cleaning.go 里的
// 适配器逐字段拷贝成 ScannerMatch。

// 清洗阶段。三阶段分别对应问题正文、思维链、答案。
const (
	StageQuestion  = "question"
	StageReasoning = "reasoning"
	StageAnswer    = "answer"
)

// 命中后的处置动作。
const (
	ActionClean = "clean" // 无命中，保持原状
	ActionFlag  = "flag"  // 标记待复核
	ActionDrop  = "drop"  // 剔除样本
	ActionRetry = "retry" // 重新生成
)

var defaultStages = []string{StageQuestion, StageReasoning, StageAnswer}

// DefaultStages 返回默认清洗阶段（问题 / 思维链 / 答案）。
func DefaultStages() []string {
	out := make([]string, len(defaultStages))
	copy(out, defaultStages)
	return out
}

// ValidStage 判断阶段名是否合法。
func ValidStage(stage string) bool {
	switch strings.TrimSpace(stage) {
	case StageQuestion, StageReasoning, StageAnswer:
		return true
	}
	return false
}

// NormalizeStages 校验并归一化用户给定的阶段列表。
// 为空时返回全部三个阶段；出现非法阶段名时返回错误而不是静默丢弃，
// 否则用户会以为某个阶段被清洗过、实际没有。
func NormalizeStages(stages []string) ([]string, error) {
	if len(stages) == 0 {
		return DefaultStages(), nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(stages))
	for _, stage := range stages {
		stage = strings.TrimSpace(stage)
		if stage == "" {
			continue
		}
		if !ValidStage(stage) {
			return nil, fmt.Errorf("unknown cleaning stage %q, expected one of question/reasoning/answer", stage)
		}
		if seen[stage] {
			continue
		}
		seen[stage] = true
		out = append(out, stage)
	}
	if len(out) == 0 {
		return DefaultStages(), nil
	}
	return out, nil
}

// ScannerMatch 一条命中记录。字段与 L11 的 cleaning.Match 一一对应，
// 但刻意独立声明，避免两个 lane 合并时同包重复声明。
type ScannerMatch struct {
	KeywordID   int64
	Pattern     string
	Category    string
	MatchedText string
	Snippet     string
	Severity    string
}

// Matcher 内容匹配器。生产实现由 worker 里的适配器提供，
// 单测用 fake 实现，因此扫描逻辑与关键词库实现解耦。
type Matcher interface {
	Match(content string) []ScannerMatch
}

// ScanTarget 一条待扫描内容。Stage 取 question / reasoning / answer。
type ScanTarget struct {
	QuestionID int64
	Stage      string
	Content    string
}

// ScanResult 单条内容的扫描结论。
type ScanResult struct {
	Target  ScanTarget
	Matches []ScannerMatch
	Action  string
}

// RuleSpec 规则规格。StageScope 为空表示对所有阶段生效。
type RuleSpec struct {
	Name       string
	StageScope []string
	MinHits    int
	Action     string
	Priority   int
}

// Scan 逐条扫描目标内容并按规则判定动作。
//
// 判定顺序：先按 stage_scope 过滤掉不适用于当前阶段的规则，再按 Priority
// **降序**（数值大者优先）稳定排序，取第一条 min_hits 满足的规则作为最终动作。
// 命中但没有任何规则覆盖时保守返回 flag——不静默丢弃数据。
func Scan(targets []ScanTarget, matcher Matcher, rules []RuleSpec) []ScanResult {
	results := make([]ScanResult, 0, len(targets))
	for _, target := range targets {
		result := ScanResult{Target: target}
		if matcher != nil {
			result.Matches = matcher.Match(target.Content)
		}
		result.Action = decideAction(target.Stage, result.Matches, rules)
		results = append(results, result)
	}
	return results
}

func decideAction(stage string, matches []ScannerMatch, rules []RuleSpec) string {
	if len(matches) == 0 {
		return ActionClean
	}
	applicable := make([]RuleSpec, 0, len(rules))
	for _, rule := range rules {
		if ruleAppliesTo(rule, stage) {
			applicable = append(applicable, rule)
		}
	}
	sort.SliceStable(applicable, func(i, j int) bool {
		if applicable[i].Priority != applicable[j].Priority {
			return applicable[i].Priority > applicable[j].Priority
		}
		return applicable[i].Name < applicable[j].Name
	})
	for _, rule := range applicable {
		if len(matches) < rule.minHitsOrDefault() {
			continue
		}
		return normalizeAction(rule.Action)
	}
	return ActionFlag
}

func (r RuleSpec) minHitsOrDefault() int {
	if r.MinHits <= 0 {
		return 1
	}
	return r.MinHits
}

func ruleAppliesTo(rule RuleSpec, stage string) bool {
	if len(rule.StageScope) == 0 {
		return true
	}
	for _, scope := range rule.StageScope {
		if strings.TrimSpace(scope) == stage {
			return true
		}
	}
	return false
}

func normalizeAction(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case ActionDrop:
		return ActionDrop
	case ActionRetry:
		return ActionRetry
	default:
		// flag / 空值 / 无法识别的动作一律按 flag 处理，避免误删数据。
		return ActionFlag
	}
}

// FindingsFromResults 把扫描结论展开成逐条命中明细，供落库与前端展示。
func FindingsFromResults(runID, datasetID int64, results []ScanResult) []model.CleaningFinding {
	findings := []model.CleaningFinding{}
	for _, result := range results {
		for _, match := range result.Matches {
			findings = append(findings, model.CleaningFinding{
				CleaningRunID: runID,
				DatasetID:     datasetID,
				QuestionID:    result.Target.QuestionID,
				Stage:         result.Target.Stage,
				KeywordID:     match.KeywordID,
				MatchedText:   match.MatchedText,
				Snippet:       match.Snippet,
				Action:        result.Action,
			})
		}
	}
	return findings
}

// CleaningStatusUpdates 汇总每个问题应写入 questions.cleaning_status 的新状态。
// drop 优先于 flag：同一问题在任一阶段被判 drop 就是 drop。
func CleaningStatusUpdates(results []ScanResult) map[int64]string {
	updates := map[int64]string{}
	for _, result := range results {
		if len(result.Matches) == 0 || result.Target.QuestionID <= 0 {
			continue
		}
		next := ActionFlag
		if result.Action == ActionDrop {
			next = ActionDrop
		}
		if updates[result.Target.QuestionID] == ActionDrop {
			continue
		}
		updates[result.Target.QuestionID] = next
	}
	return updates
}

// BuildReport 生成清洗报告：分阶段统计 + Top 命中关键词 + 文字结论。
// scanned 为 0 的阶段 HitRate 保持 0，不做除法，避免除零 panic。
func BuildReport(run model.CleaningRun, results []ScanResult) model.CleaningReport {
	stages := run.Stages
	if len(stages) == 0 {
		stages = DefaultStages()
	}
	stats := make([]model.CleaningStageStat, 0, len(stages))
	totalDropped := 0
	for _, stage := range stages {
		stat := model.CleaningStageStat{Stage: stage}
		for _, result := range results {
			if result.Target.Stage != stage {
				continue
			}
			stat.ScannedItems++
			if len(result.Matches) > 0 {
				stat.FlaggedItems++
			}
			if result.Action == ActionDrop {
				stat.DroppedItems++
			}
		}
		if stat.ScannedItems > 0 {
			stat.HitRate = float64(stat.FlaggedItems) / float64(stat.ScannedItems)
		}
		totalDropped += stat.DroppedItems
		stats = append(stats, stat)
	}

	top := TopKeywords(results, 20)
	return model.CleaningReport{
		Run:         run,
		Stages:      stats,
		TopKeywords: top,
		Conclusions: buildConclusions(stats, top, totalDropped),
		GeneratedAt: time.Now(),
	}
}

// TopKeywords 按命中次数倒序聚合关键词，limit<=0 表示不截断。
func TopKeywords(results []ScanResult, limit int) []model.CleaningKeywordStat {
	index := map[string]*model.CleaningKeywordStat{}
	order := []string{}
	for _, result := range results {
		for _, match := range result.Matches {
			key := fmt.Sprintf("%d|%s|%s", match.KeywordID, match.Pattern, match.Category)
			entry, ok := index[key]
			if !ok {
				entry = &model.CleaningKeywordStat{
					KeywordID:   match.KeywordID,
					Pattern:     match.Pattern,
					Category:    match.Category,
					SampleSnipp: match.Snippet,
				}
				index[key] = entry
				order = append(order, key)
			}
			entry.Hits++
		}
	}

	out := make([]model.CleaningKeywordStat, 0, len(order))
	for _, key := range order {
		out = append(out, *index[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].Pattern < out[j].Pattern
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func buildConclusions(stats []model.CleaningStageStat, top []model.CleaningKeywordStat, totalDropped int) []string {
	conclusions := []string{}
	for _, stat := range stats {
		switch {
		case stat.ScannedItems == 0:
			conclusions = append(conclusions, fmt.Sprintf("「%s」阶段无可清洗数据，未执行扫描", stageLabel(stat.Stage)))
		case stat.FlaggedItems == 0:
			conclusions = append(conclusions, fmt.Sprintf("「%s」阶段命中率 0%%，未发现拒答或违规内容，质量良好", stageLabel(stat.Stage)))
		case stat.DroppedItems > 0:
			conclusions = append(conclusions, fmt.Sprintf("「%s」阶段命中 %d 条（命中率 %.1f%%），其中 %d 条已按 drop 规则剔除，建议重新生成",
				stageLabel(stat.Stage), stat.FlaggedItems, stat.HitRate*100, stat.DroppedItems))
		default:
			conclusions = append(conclusions, fmt.Sprintf("「%s」阶段命中 %d 条（命中率 %.1f%%），已标记待人工复核",
				stageLabel(stat.Stage), stat.FlaggedItems, stat.HitRate*100))
		}
	}
	if len(top) > 0 {
		conclusions = append(conclusions, fmt.Sprintf("命中最多的是「%s」（%s 类，%d 次）", top[0].Pattern, top[0].Category, top[0].Hits))
	}
	if totalDropped == 0 {
		conclusions = append(conclusions, "本次清洗未剔除任何样本，数据集可继续使用")
	} else {
		conclusions = append(conclusions, fmt.Sprintf("本次清洗共剔除 %d 条样本，导出前请确认", totalDropped))
	}
	return conclusions
}

func stageLabel(stage string) string {
	switch stage {
	case StageQuestion:
		return "问题"
	case StageReasoning:
		return "思维链"
	case StageAnswer:
		return "答案"
	default:
		return stage
	}
}
