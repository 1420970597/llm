package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/1420970597/llm/internal/cleaning"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件实现 L12 的 worker 侧：按用户指定的阶段（问题 / 思维链 / 答案）
// 逐条扫描数据集内容，拦截拒答，落库命中明细并生成清洗报告。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.12 节。

const cleaningRunStage = "cleaning.run"

func init() {
	RegisterJobHandler(cleaningRunStage, handleCleaningRun)
}

func handleCleaningRun(ctx context.Context, jc *jobContext, job jobPayload) error {
	runs := store.NewCleaningRunStore(jc.db())

	run, err := runs.ActiveRun(ctx, job.DatasetID)
	if err != nil {
		if store.IsCleaningRunNotFound(err) {
			log.Printf("cleaning.run.no_active_run dataset=%d", job.DatasetID)
			return nil
		}
		return err
	}

	if err := runCleaning(ctx, jc, runs, run); err != nil {
		_ = runs.MarkFailed(ctx, run.ID, err.Error())
		return err
	}
	return nil
}

func runCleaning(ctx context.Context, jc *jobContext, runs *store.CleaningRunStore, run model.CleaningRun) error {
	stages := run.Stages
	if len(stages) == 0 {
		stages = cleaning.DefaultStages()
	}

	// 与冻结契约一致：异步任务同时落 generation_runs，前端据此统一轮询进度。
	progress, err := jc.generationRuns.StartRun(ctx, run.DatasetID, cleaningRunStage, len(stages))
	if err != nil {
		return err
	}
	finish := func(status, summary string) {
		if err := jc.generationRuns.FinishRun(ctx, progress.ID, status, summary); err != nil {
			log.Printf("cleaning.run.finish_progress_failed run=%d err=%v", run.ID, err)
		}
	}

	sources, err := runs.LoadScanSources(ctx, run.DatasetID)
	if err != nil {
		finish("failed", err.Error())
		return err
	}
	if len(sources.Questions) == 0 {
		report := cleaning.BuildReport(run, nil)
		if err := runs.MarkDone(ctx, run.ID, 0, 0, 0, report); err != nil {
			return err
		}
		finish("completed", "")
		log.Printf("cleaning.run.empty dataset=%d", run.DatasetID)
		return nil
	}

	keywords, err := loadActiveKeywords(ctx, jc)
	if err != nil {
		finish("failed", err.Error())
		return err
	}
	rules, err := loadCleaningRules(ctx, jc)
	if err != nil {
		finish("failed", err.Error())
		return err
	}

	targets := buildScanTargets(sources, stages)
	results := cleaning.Scan(targets, newKeywordMatcher(keywords), rules)

	findings := cleaning.FindingsFromResults(run.ID, run.DatasetID, results)
	if err := runs.InsertFindings(ctx, run.ID, run.DatasetID, findings); err != nil {
		finish("failed", err.Error())
		return err
	}

	statusUpdates := cleaning.CleaningStatusUpdates(results)
	dropped, err := runs.ApplyCleaningStatus(ctx, run.DatasetID, statusUpdates)
	if err != nil {
		finish("failed", err.Error())
		return err
	}

	scanned, flagged := 0, 0
	for _, result := range results {
		scanned++
		if len(result.Matches) > 0 {
			flagged++
		}
	}

	report := cleaning.BuildReport(run, results)
	if err := runs.MarkDone(ctx, run.ID, scanned, flagged, dropped, report); err != nil {
		finish("failed", err.Error())
		return err
	}
	finish("completed", "")

	log.Printf("cleaning.run.done dataset=%d run=%d scanned=%d flagged=%d dropped=%d stages=%v findings=%d",
		run.DatasetID, run.ID, scanned, flagged, dropped, stages, len(findings))
	return nil
}

// buildScanTargets 把三阶段数据源展开成扫描目标，只保留用户选中的阶段。
func buildScanTargets(sources store.ScanSources, stages []string) []cleaning.ScanTarget {
	selected := map[string]bool{}
	for _, stage := range stages {
		selected[stage] = true
	}

	targets := []cleaning.ScanTarget{}
	for _, question := range sources.Questions {
		if selected[cleaning.StageQuestion] {
			targets = append(targets, cleaning.ScanTarget{
				QuestionID: question.ID,
				Stage:      cleaning.StageQuestion,
				Content:    question.Content,
			})
		}
		if selected[cleaning.StageReasoning] {
			if thought := sources.Reasoning[question.ID]; thought != "" {
				targets = append(targets, cleaning.ScanTarget{
					QuestionID: question.ID,
					Stage:      cleaning.StageReasoning,
					Content:    thought,
				})
			}
		}
		if selected[cleaning.StageAnswer] {
			if answer := sources.Answers[question.ID]; answer != "" {
				targets = append(targets, cleaning.ScanTarget{
					QuestionID: question.ID,
					Stage:      cleaning.StageAnswer,
					Content:    answer,
				})
			}
		}
	}
	return targets
}

func loadActiveKeywords(ctx context.Context, jc *jobContext) ([]model.CleaningKeyword, error) {
	rows, err := jc.db().Query(ctx, `
    SELECT id, pattern, category, match_mode, severity, is_builtin, is_active, note
    FROM cleaning_keywords WHERE is_active = TRUE ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.CleaningKeyword{}
	for rows.Next() {
		var item model.CleaningKeyword
		if err := rows.Scan(&item.ID, &item.Pattern, &item.Category, &item.MatchMode,
			&item.Severity, &item.IsBuiltin, &item.IsActive, &item.Note); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func loadCleaningRules(ctx context.Context, jc *jobContext) ([]cleaning.RuleSpec, error) {
	rows, err := jc.db().Query(ctx, `
    SELECT name, stage_scope, min_hits, action, priority
    FROM cleaning_rules WHERE is_active = TRUE ORDER BY priority DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	specs := []cleaning.RuleSpec{}
	for rows.Next() {
		var spec cleaning.RuleSpec
		var scopePayload []byte
		if err := rows.Scan(&spec.Name, &scopePayload, &spec.MinHits, &spec.Action, &spec.Priority); err != nil {
			return nil, err
		}
		if len(scopePayload) > 0 {
			if err := json.Unmarshal(scopePayload, &spec.StageScope); err != nil {
				return nil, fmt.Errorf("rule %s has invalid stage_scope: %w", spec.Name, err)
			}
		}
		specs = append(specs, spec)
	}
	return specs, rows.Err()
}

// 关键词匹配适配器。
//
// ponytail: 这里是 L11（internal/cleaning/keywords.go）落地前的临时实现，
// 用 model.CleaningKeyword 自身做 contains / prefix / regex 匹配。
// L11 合并后应替换为 cleaning.MatchKeywords(content, keywords) + cleaning.Snippet，
// 以复用 L11 的全角半角归一化与内置关键词库。待接线点仅此一处。
type keywordMatcher struct {
	keywords []model.CleaningKeyword
	regexes  map[int64]*regexp.Regexp
}

func newKeywordMatcher(keywords []model.CleaningKeyword) *keywordMatcher {
	matcher := &keywordMatcher{keywords: keywords, regexes: map[int64]*regexp.Regexp{}}
	for _, keyword := range keywords {
		if keyword.MatchMode != "regex" {
			continue
		}
		if compiled, err := regexp.Compile(keyword.Pattern); err == nil {
			matcher.regexes[keyword.ID] = compiled
		}
	}
	return matcher
}

func (m *keywordMatcher) Match(content string) []cleaning.ScannerMatch {
	matches := []cleaning.ScannerMatch{}
	if strings.TrimSpace(content) == "" {
		return matches
	}
	for _, keyword := range m.keywords {
		pattern := strings.TrimSpace(keyword.Pattern)
		if pattern == "" {
			continue
		}

		index, end := -1, 0
		switch keyword.MatchMode {
		case "prefix":
			if strings.HasPrefix(content, pattern) {
				index, end = 0, len(pattern)
			}
		case "regex":
			compiled, ok := m.regexes[keyword.ID]
			if !ok {
				continue
			}
			if loc := compiled.FindStringIndex(content); loc != nil {
				index, end = loc[0], loc[1]
			}
		default: // contains
			if at := strings.Index(content, pattern); at >= 0 {
				index, end = at, at+len(pattern)
			}
		}
		if index < 0 {
			continue
		}

		matches = append(matches, cleaning.ScannerMatch{
			KeywordID:   keyword.ID,
			Pattern:     keyword.Pattern,
			Category:    keyword.Category,
			MatchedText: content[index:end],
			Snippet:     snippetRunes(content, index, end, 30),
			Severity:    keyword.Severity,
		})
	}
	return matches
}

// snippetRunes 截取命中位置前后各 radius 个字符的上下文。
// 必须按 rune 处理：中文是多字节，按 byte 切会切出乱码。
func snippetRunes(content string, start, end, radius int) string {
	runes := []rune(content)
	// start/end 是 byte 偏移，先换算成 rune 下标。
	startRune := len([]rune(content[:start]))
	endRune := startRune + len([]rune(content[start:end]))

	from := startRune - radius
	if from < 0 {
		from = 0
	}
	to := endRune + radius
	if to > len(runes) {
		to = len(runes)
	}
	prefix := ""
	if from > 0 {
		prefix = "…"
	}
	suffix := ""
	if to < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[from:to]) + suffix
}
