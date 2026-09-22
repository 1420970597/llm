package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/cleaning"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现版本化规则的**纯预览**与**不可变命中证据**（Issue #160 T15）。
//
// 契约：docs/plans/atelier-api-contract.md §2.6（规则预览）、
// docs/plans/atelier-implementation.md §4.1（证据与 Decision 分离）、
// sql/migrations/0029_studio_rules_evidence.sql 的文件头。
//
// 本文件最重要的性质是**没有任何隐藏写路径**：
//
//   * `PreviewRules` 在实现上是若干 SELECT —— 它不写处置、不改内容、
//     不入队列。契约 §2.6 要求「预览前后内容/处置/队列计数无变化」，
//     而这不是靠「记得回滚」保证的，是靠**根本没有写语句**。
//     旧实现（`ApplyCleaningStatus`）在扫描时直接写回
//     `questions.cleaning_status`，因此旧链路里「预览」与「执行」无法分开。
//   * `RecordRuleEvidence` **只追加**：它写执行记录与命中证据，
//     不修改任何既有的处置字段，也不写人工 Decision（T16 的表）。
//
// 因此「规则自动写人工处置」在本文件里不可能发生 —— 结构上做不到。

// RuleStore 提供规则预览与命中证据读写。
type RuleStore struct {
	db *pgxpool.Pool
}

// NewRuleStore 构造规则 store。
func NewRuleStore(db *pgxpool.Pool) *RuleStore {
	return &RuleStore{db: db}
}

// ---------------------------------------------------------------------------
// 预览（纯读）
// ---------------------------------------------------------------------------

// PreviewRules 扫描选定的样本版本并返回命中位置。
//
// 三个刻意如此的设计：
//
//  1. **只扫描被点名的内容版本**（不是「当前筛选出来的样本」）：
//     预览必须是可复现的，而「当前筛选」会随数据变化。
//  2. **规则来自被引用的质量策略版本**（不是「当前规则表」）：
//     否则改一次规则就看不到「上一版规则会命中什么」，而对比两版规则
//     正是预览最常见的用途。
//  3. **位置以字符（rune）计**：与界面高亮一致；字节偏移会让中文错位。
func (s *RuleStore) PreviewRules(ctx context.Context, projectID, policyVersionID int64, sampleVersionIDs []int64, maxHits int) (model.RulePreviewResult, error) {
	result := model.RulePreviewResult{
		PolicyVersionID: policyVersionID,
		Hits:            []model.RulePreviewHit{},
		// 恒为 false：预览没有写路径（见文件头）。
		SideEffects: false,
	}
	if len(sampleVersionIDs) == 0 {
		return result, &apiStoreError{Message: "请先选择要预览的样本版本"}
	}
	if maxHits <= 0 {
		maxHits = model.MaxRuleMatchesPerContent
	}

	rules, err := s.loadPolicyRules(ctx, projectID, policyVersionID)
	if err != nil {
		return result, err
	}

	contents, hashes, err := s.loadSampleContents(ctx, projectID, sampleVersionIDs)
	if err != nil {
		return result, err
	}
	result.ScannedCount = len(contents)

	for _, versionID := range sampleVersionIDs {
		payload, found := contents[versionID]
		if !found {
			// 版本不存在/不属于本项目：拒绝而不是跳过 —— 跳过会让「扫描了 3 个」
			// 实际只扫了 2 个而用户不知道（分母静默变小）。
			return result, &apiStoreError{Message: fmt.Sprintf(
				"样本版本 %d 不存在或不属于本项目，已拒绝预览（避免扫描范围静默变小）", versionID)}
		}
		for _, rule := range rules {
			if len(result.Hits) >= maxHits {
				result.Truncated = true
				return result, nil
			}
			hits := previewRule(rule, payload, maxHits-len(result.Hits))
			for _, hit := range hits {
				result.Hits = append(result.Hits, hit)
				if len(result.Hits) >= maxHits {
					result.Truncated = true
					break
				}
			}
		}
	}
	_ = hashes
	return result, nil
}

// previewRule 在一条内容的某个字段上应用一条规则。
//
// **复用 internal/cleaning 的匹配器**（T15 验收项要求）而不是另写一套：
// 两份实现必然在归一化、大小写、中文片段这些细节上漂移，
// 而漂移的表现是「预览说会命中、执行时没命中」——最难排查的一类不一致。
func previewRule(rule model.QualityRule, payload json.RawMessage, budget int) []model.RulePreviewHit {
	fieldText := fieldTextFromPayload(payload, rule.Field)
	if fieldText == "" {
		// 字段为空时：
		//   * field_check/structure 这类「必须非空」的规则**应当命中**
		//     （空字段正是它们要找的问题）；
		//   * 关键词/正则类规则对空内容没有可命中的位置，跳过。
		if isEmptinessCheck(rule) {
			return []model.RulePreviewHit{{
				RuleID: rule.ID, RuleName: rule.Name, Field: rule.Field,
				Severity: rule.Severity, SuggestedAction: rule.SuggestedAction,
				MatchStart: 0, MatchEnd: 0, Snippet: "（该字段为空）",
			}}
		}
		return nil
	}
	if len([]rune(fieldText)) > model.MaxRuleContentLength {
		// 超长内容不匹配（服务端上限）：返回一条显式说明而不是静默无命中，
		// 否则用户会以为「规则没命中」而真实原因是内容太长被跳过。
		return []model.RulePreviewHit{{
			RuleID: rule.ID, RuleName: rule.Name, Field: rule.Field,
			Severity: model.RuleSeverityWarning, SuggestedAction: model.RuleActionReview,
			MatchStart: 0, MatchEnd: 0,
			Snippet: fmt.Sprintf("（字段内容超长，超过 %d 字符上限，未执行匹配）", model.MaxRuleContentLength),
		}}
	}

	if isEmptinessCheck(rule) {
		// 有内容 → 「必须非空」的检查不命中。
		return nil
	}

	keywords := keywordsForRule(rule)
	if len(keywords) == 0 {
		return nil
	}
	matches := cleaning.MatchKeywordsAll(fieldText, keywords, budget)
	hits := make([]model.RulePreviewHit, 0, len(matches))
	for _, match := range matches {
		hits = append(hits, model.RulePreviewHit{
			RuleID: rule.ID, RuleName: rule.Name, Field: rule.Field,
			// 严重度与建议动作取自**规则快照**（版本），不是「当前规则表」。
			Severity: rule.Severity, SuggestedAction: rule.SuggestedAction,
			MatchStart: match.MatchStart, MatchEnd: match.MatchEnd, Snippet: match.Snippet,
		})
	}
	return hits
}

// keywordsForRule 把一条规则映射成清洗匹配器可用的关键词集合。
//
// 一个规则可以带多个关键词快照（T15：规则版本包含关键词内容快照），
// 因此这里可能产出多条。关键词快照的存在使历史命中保留**当时的**关键词，
// 不跟随关键词表漂移。
func keywordsForRule(rule model.QualityRule) []model.CleaningKeyword {
	mode := cleaning.ModeContains
	switch rule.MatchType {
	case model.RuleMatchRegex:
		mode = cleaning.ModeRegex
	case model.RuleMatchContains:
		mode = cleaning.ModeContains
	default:
		// field_check / structure 不是文本匹配，由 isEmptinessCheck 处理。
		return nil
	}

	keywords := []model.CleaningKeyword{}
	if strings.TrimSpace(rule.Expression) != "" {
		keywords = append(keywords, model.CleaningKeyword{
			Pattern: rule.Expression, MatchMode: mode, Severity: rule.Severity, IsActive: true,
			Category: rule.ID,
		})
	}
	for _, keyword := range rule.Keywords {
		if strings.TrimSpace(keyword) == "" {
			continue
		}
		keywords = append(keywords, model.CleaningKeyword{
			Pattern: keyword, MatchMode: cleaning.ModeContains, Severity: rule.Severity,
			IsActive: true, Category: rule.ID,
		})
	}
	return keywords
}

// isEmptinessCheck 判断规则是否是「字段必须非空」这类检查。
//
// 只认两种明确写法（`len>0` / `len>=1`）：把任意表达式都当检查会让
// 「表达式解析」变成一个隐性的 DSL，而 §5 明确禁止任意脚本节点。
// 认不出来的写法按「不做匹配」处理（返回 nil），并在规则校验阶段
// 由 matchType 的闭合集合挡住 —— 不静默猜语义。
func isEmptinessCheck(rule model.QualityRule) bool {
	if rule.MatchType != model.RuleMatchFieldCheck && rule.MatchType != model.RuleMatchStructure {
		return false
	}
	expression := strings.ReplaceAll(strings.TrimSpace(rule.Expression), " ", "")
	return expression == "len>0" || expression == "len>=1"
}

// fieldTextFromPayload 从样本 payload 里取出规则作用的字段文本。
//
// 取不到时返回空串（由调用方按规则类型决定是「命中」还是「跳过」）——
// 不报错：一条规则引用了一个本样本没有的字段是**正常**形态
// （SFT 与 GRPO 的字段集不同，同一份质量策略可能同时被两者引用）。
func fieldTextFromPayload(payload json.RawMessage, field string) string {
	if len(payload) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return ""
	}
	value, found := object[field]
	if !found {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		// 非字符串字段（数组/对象）序列化后再匹配：规则表达式是按文本写的，
		// 而「字段是数组」不应让规则静默失效。
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// loadPolicyRules 读取质量策略版本的规则清单（项目作用域校验）。
func (s *RuleStore) loadPolicyRules(ctx context.Context, projectID, versionID int64) ([]model.QualityRule, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `
    SELECT payload FROM document_versions
    WHERE id = $1 AND project_id = $2 AND kind = 'quality_policy'`, versionID, projectID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &apiStoreError{Message: "质量策略版本不存在或不属于本项目"}
	}
	if err != nil {
		return nil, err
	}
	var payload model.QualityPolicyPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, &apiStoreError{Message: "质量策略版本内容无法解析，请重新保存策略"}
	}
	return payload.Rules, nil
}

// loadSampleContents 读取样本版本内容（项目作用域）。
func (s *RuleStore) loadSampleContents(ctx context.Context, projectID int64, versionIDs []int64) (map[int64]json.RawMessage, map[int64]string, error) {
	rows, err := s.db.Query(ctx, `
    SELECT id, payload, content_hash FROM sample_versions
    WHERE project_id = $1 AND id = ANY($2::bigint[])`, projectID, versionIDs)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	contents := map[int64]json.RawMessage{}
	hashes := map[int64]string{}
	for rows.Next() {
		var id int64
		var payload json.RawMessage
		var hash string
		if err := rows.Scan(&id, &payload, &hash); err != nil {
			return nil, nil, err
		}
		contents[id] = payload
		hashes[id] = hash
	}
	return contents, hashes, rows.Err()
}

// ---------------------------------------------------------------------------
// 证据（只追加）
// ---------------------------------------------------------------------------

// RecordRuleEvaluationInput 是「执行一次规则检查并留下证据」的请求。
type RecordRuleEvaluationInput struct {
	ProjectID              int64
	QualityPolicyVersionID int64
	CreatedBy              *int64
	// Evidence 是已经算好的命中证据（通常来自一次 PreviewRules）。
	Evidence     []model.RuleEvidence
	ScannedCount int
}

// RecordRuleEvaluation 写入一次执行记录与其命中证据。
//
// **只追加**：不修改任何既有处置、不改内容、不写人工 Decision。
// 返回的 evaluationID 是证据的归属：没有它，命中会变成一堆无法归属到
// 某次动作的孤立行（「什么时候扫的、用哪一版规则」都答不上来）。
func (s *RuleStore) RecordRuleEvaluation(ctx context.Context, input RecordRuleEvaluationInput) (int64, error) {
	if input.ProjectID <= 0 {
		return 0, &apiStoreError{Message: "规则执行必须归属到一个项目"}
	}
	if input.QualityPolicyVersionID <= 0 {
		return 0, &apiStoreError{Message: "规则执行必须引用一个质量策略版本"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var evaluationID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO rule_evaluations
      (project_id, quality_policy_version_id, purpose, status, scanned_count, hit_count, created_by, finished_at)
    VALUES ($1, $2, 'experiment', 'completed', $3, $4, $5, NOW())
    RETURNING id`,
		input.ProjectID, input.QualityPolicyVersionID, input.ScannedCount, len(input.Evidence),
		input.CreatedBy).Scan(&evaluationID); err != nil {
		return 0, err
	}

	for _, evidence := range input.Evidence {
		if _, err := tx.Exec(ctx, `
      INSERT INTO rule_evidence
        (evaluation_id, project_id, sample_id, sample_version_id, content_hash,
         rule_id, rule_name, rule_expression, rule_match_type, severity, suggested_action,
         field_name, match_start, match_end, snippet)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			evaluationID, input.ProjectID, evidence.SampleID, evidence.SampleVersionID,
			evidence.ContentHash, evidence.RuleID, evidence.RuleName, evidence.RuleExpression,
			evidence.RuleMatchType, evidence.Severity, evidence.SuggestedAction,
			evidence.FieldName, evidence.MatchStart, evidence.MatchEnd, evidence.Snippet); err != nil {
			return 0, err
		}
	}

	// 审计与证据同事务：不允许「写了证据但没有审计记录」。
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: derefInt64(input.CreatedBy), Action: "rule_evidence_recorded",
		Resource: "rule_evaluation", ResourceID: fmt.Sprint(evaluationID), ProjectID: input.ProjectID,
		Reason: fmt.Sprintf("policyVersion=%d scanned=%d hits=%d",
			input.QualityPolicyVersionID, input.ScannedCount, len(input.Evidence)),
	}); err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return evaluationID, nil
}

// ListRuleEvidence 列出某个样本版本的命中证据。
//
// 返回的是**当时的快照**（表达式/严重度/建议动作都在行内），
// 因此规则后来被改/删都不会改变这些行显示的内容 —— 这正是「误报能被人工
// 记录理由处理，证据仍保留」所依赖的性质。
func (s *RuleStore) ListRuleEvidence(ctx context.Context, projectID, sampleVersionID int64, limit int) ([]model.RuleEvidence, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, evaluation_id, project_id, sample_id, sample_version_id, content_hash,
           rule_id, rule_name, rule_expression, rule_match_type, severity, suggested_action,
           field_name, match_start, match_end, snippet
    FROM rule_evidence
    WHERE project_id = $1 AND sample_version_id = $2
    ORDER BY id LIMIT $3`, projectID, sampleVersionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.RuleEvidence{}
	for rows.Next() {
		var item model.RuleEvidence
		if err := rows.Scan(&item.ID, &item.EvaluationID, &item.ProjectID, &item.SampleID,
			&item.SampleVersionID, &item.ContentHash, &item.RuleID, &item.RuleName,
			&item.RuleExpression, &item.RuleMatchType, &item.Severity, &item.SuggestedAction,
			&item.FieldName, &item.MatchStart, &item.MatchEnd, &item.Snippet); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
