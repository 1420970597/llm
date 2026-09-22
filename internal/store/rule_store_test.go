package store

import (
	"context"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T15 的规则预览与命中证据。
//
// 必须连真实 Postgres：核心断言是「预览前后**数据库**无变化」
//（不是「我们没有调用写方法」）与「证据行不随规则变化」。
// 内存实现测不出这两条。

type ruleFixture struct {
	pool      *pgxpool.Pool
	projectID int64
	userID    int64
	policyID  int64
	versions  []model.SampleVersion
	rules     *RuleStore
	batches   *BatchStore
}

// newRuleFixture 复用批次 fixture（已含质量策略版本），再补几个样本版本。
func newRuleFixture(t *testing.T) ruleFixture {
	t.Helper()
	base := newBatchFixture(t)
	ctx := context.Background()

	batches := NewBatchStore(base.pool)
	versions := []model.SampleVersion{}
	// 三条内容：一条含拒答模板（应命中关键词规则），一条正常，一条字段为空。
	payloads := []map[string]any{
		{"question": "冷链温控怎么做", "reasoning": "先识别约束", "answer": "抱歉，我不能回答这个问题。"},
		{"question": "冷链追溯怎么做", "reasoning": "先识别约束", "answer": "通过批次号与温度记录实现追溯。"},
		{"question": "空答案样例", "reasoning": "先识别约束", "answer": ""},
	}
	for index, payload := range payloads {
		key := "rule-item-" + strings.ReplaceAll(t.Name(), "/", "_") + "-" + string(rune('a'+index))
		sample, err := batches.EnsureSample(ctx, base.projectID, key, model.TargetKindSFT, key, nil)
		if err != nil {
			t.Fatalf("EnsureSample: %v", err)
		}
		_, version, err := batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
			ProjectID: base.projectID, SampleKey: sample.SampleKey,
			TargetKind: model.TargetKindSFT, Title: key, Payload: payload,
		})
		if err != nil {
			t.Fatalf("AppendSampleVersion: %v", err)
		}
		versions = append(versions, version)
	}

	return ruleFixture{
		pool: base.pool, projectID: base.projectID, userID: base.editorID,
		policyID: base.policyID, versions: versions,
		rules: NewRuleStore(base.pool), batches: batches,
	}
}

// versionIDs 返回全部样本版本 ID。
func (fixture ruleFixture) versionIDs() []int64 {
	ids := make([]int64, 0, len(fixture.versions))
	for _, version := range fixture.versions {
		ids = append(ids, version.ID)
	}
	return ids
}

// savePolicyWithRules 保存一版带指定规则的质量策略，返回版本行 ID。
//
// 必须带 `ExpectedRevision`（乐观锁）：不带时默认 0，而 fixture 里已经存在
// 一版质量策略，于是保存会被 409 拒绝 ——「版本已变化，请重新加载后再保存」。
// 这里按 head 记录的当前 revision 传入，与真实调用方的做法一致。
func (fixture ruleFixture) savePolicyWithRules(t *testing.T, rules []model.QualityRule) (int64, error) {
	t.Helper()
	documents := NewDocumentStore(fixture.pool)
	ctx := context.Background()

	revision := int64(0)
	if document, err := documents.GetDocument(ctx, fixture.projectID, model.KindQualityPolicy,
		DefaultLogicalID); err == nil {
		revision = document.RowVersion
	}

	payload := model.QualityPolicyPayload{
		SchemaVersion: model.SchemaVersionFor(model.KindQualityPolicy),
		Rules:         rules,
	}
	_, version, err := documents.SaveVersion(ctx, fixture.projectID,
		model.KindQualityPolicy, fixture.userID, SaveDocumentVersionInput{
			ExpectedRevision: revision, ChangeReason: "测试规则", Payload: payload,
		})
	if err != nil {
		return 0, err
	}
	return version.ID, nil
}

// mustSavePolicy 保存策略并要求成功（多数用例的用法）。
func (fixture ruleFixture) mustSavePolicy(t *testing.T, rules []model.QualityRule) int64 {
	t.Helper()
	versionID, err := fixture.savePolicyWithRules(t, rules)
	if err != nil {
		t.Fatalf("SaveVersion: %v", err)
	}
	return versionID
}

// TestPreviewRulesHasNoSideEffects 覆盖验收项
// 「预览前后内容/处置/队列计数无变化」。
//
// 断言的是**数据库层面的无变化**，而不是「没有调用写方法」：
// 后者只能证明我们记得不写，前者才能证明预览真的没写。
func TestPreviewRulesHasNoSideEffects(t *testing.T) {
	fixture := newRuleFixture(t)
	ctx := context.Background()
	policyID := fixture.mustSavePolicy(t, []model.QualityRule{{
		ID: "refusal", Name: "拒答模板", MatchType: model.RuleMatchContains,
		Expression: "抱歉，我不能回答", Field: "answer",
		Severity: model.RuleSeverityError, SuggestedAction: model.RuleActionReview,
	}})

	// 快照「预览前」的计数。
	before := ruleCounts(t, fixture)

	result, err := fixture.rules.PreviewRules(ctx, fixture.projectID, policyID, fixture.versionIDs(), 100)
	if err != nil {
		t.Fatalf("PreviewRules: %v", err)
	}

	// 命中必须带位置：命中「抱歉，我不能回答」在 answer 字段里。
	if len(result.Hits) != 1 {
		t.Fatalf("应命中 1 处，实际 %d：%+v", len(result.Hits), result.Hits)
	}
	hit := result.Hits[0]
	if hit.Field != "answer" {
		t.Fatalf("命中字段应为 answer，实际 %q", hit.Field)
	}
	if hit.RuleID != "refusal" || hit.Severity != model.RuleSeverityError {
		t.Fatalf("命中必须带规则快照，实际 %+v", hit)
	}
	// 位置以**字符**计：内容「抱歉，我不能回答这个问题。」的前 8 个字命中。
	if hit.MatchStart != 0 || hit.MatchEnd != 8 {
		t.Fatalf("命中位置应为 0–8（按字符），实际 %d–%d", hit.MatchStart, hit.MatchEnd)
	}
	if result.SideEffects {
		// 响应必须显式声明无副作用（前端与验收脚本据此断言）。
		t.Fatal("响应必须显式带 sideEffects: false")
	}
	if result.ScannedCount != 3 {
		t.Fatalf("应扫描 3 个版本，实际 %d", result.ScannedCount)
	}

	// 关键断言：预览前后数据库无变化。
	after := ruleCounts(t, fixture)
	// map 不能直接用 != 比较，逐键比对（并给出可读的差异）。
	for name, beforeCount := range before {
		if after[name] != beforeCount {
			t.Fatalf("预览不得改变 %s 的计数：预览前 %d，预览后 %d", name, beforeCount, after[name])
		}
	}
}

// ruleCounts 汇总「预览可能影响到的」各类计数。
//
// 刻意包含 rule_evidence（预览不得写证据）与 samples（不得改样本状态）：
// 旧实现（ApplyCleaningStatus）正是在扫描时写回 questions.cleaning_status，
// 因此「预览是否真的只读」必须从这些表上看。
func ruleCounts(t *testing.T, fixture ruleFixture) map[string]int {
	t.Helper()
	ctx := context.Background()
	counts := map[string]int{}
	queries := map[string]string{
		"rule_evidence":    `SELECT COUNT(*) FROM rule_evidence WHERE project_id = $1`,
		"rule_evaluations": `SELECT COUNT(*) FROM rule_evaluations WHERE project_id = $1`,
		"samples":          `SELECT COUNT(*) FROM samples WHERE project_id = $1`,
		"sample_versions":  `SELECT COUNT(*) FROM sample_versions WHERE project_id = $1`,
		"batch_items":      `SELECT COUNT(*) FROM batch_items WHERE project_id = $1`,
	}
	for name, query := range queries {
		var count int
		if err := fixture.pool.QueryRow(ctx, query, fixture.projectID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		counts[name] = count
	}
	return counts
}

// TestPreviewRulesReportsAllPositions 覆盖「命中位置」不止第一处。
func TestPreviewRulesReportsAllPositions(t *testing.T) {
	fixture := newRuleFixture(t)
	ctx := context.Background()
	policyID := fixture.mustSavePolicy(t, []model.QualityRule{{
		ID: "echo", Name: "重复词", MatchType: model.RuleMatchContains,
		Expression: "追溯", Field: "answer",
		Severity: model.RuleSeverityWarning, SuggestedAction: model.RuleActionReview,
	}})

	// 另建一条含两处「追溯」的内容。
	sample, err := fixture.batches.EnsureSample(ctx, fixture.projectID, "multi-hit-"+t.Name(),
		model.TargetKindSFT, "多处命中", nil)
	if err != nil {
		t.Fatalf("EnsureSample: %v", err)
	}
	_, version, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: sample.SampleKey, TargetKind: model.TargetKindSFT,
		// SFT payload 必须有 question/reasoning/answer（T05 的 schema 校验）：
		// 只给 answer 会被拒，而那只说明我构造的夹具不合法，不是被测行为的问题。
		Title: "多处命中",
		Payload: map[string]any{
			"question": "冷链追溯怎么做", "reasoning": "先识别约束",
			"answer": "追溯靠批次号，追溯也靠温度记录。",
		},
	})
	if err != nil {
		t.Fatalf("AppendSampleVersion: %v", err)
	}

	result, err := fixture.rules.PreviewRules(ctx, fixture.projectID, policyID, []int64{version.ID}, 100)
	if err != nil {
		t.Fatalf("PreviewRules: %v", err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("两处「追溯」都必须被报出（只报第一处会让用户以为只有一处），实际 %d：%+v",
			len(result.Hits), result.Hits)
	}
	if result.Hits[0].MatchStart == result.Hits[1].MatchStart {
		t.Fatalf("两处命中的位置必须不同：%+v", result.Hits)
	}
	if result.Hits[0].MatchStart > result.Hits[1].MatchStart {
		t.Fatalf("命中必须按位置升序，实际 %+v", result.Hits)
	}
}

// TestPreviewRulesMarksTruncation 覆盖「命中达到上限必须显式告知」。
func TestPreviewRulesMarksTruncation(t *testing.T) {
	fixture := newRuleFixture(t)
	ctx := context.Background()
	policyID := fixture.mustSavePolicy(t, []model.QualityRule{{
		ID: "any", Name: "任意字符", MatchType: model.RuleMatchRegex,
		Expression: ".", Field: "answer",
		Severity: model.RuleSeverityInfo, SuggestedAction: model.RuleActionSuggestQuarantine,
	}})

	result, err := fixture.rules.PreviewRules(ctx, fixture.projectID, policyID, fixture.versionIDs(), 5)
	if err != nil {
		t.Fatalf("PreviewRules: %v", err)
	}
	if len(result.Hits) != 5 {
		t.Fatalf("上限 5 时应恰好返回 5 条，实际 %d", len(result.Hits))
	}
	if !result.Truncated {
		t.Fatal("达到上限必须标 truncated（否则用户会以为「就这么多命中」）")
	}
}

// TestPreviewRulesRejectsUnknownVersions 覆盖「扫描范围不得静默变小」。
func TestPreviewRulesRejectsUnknownVersions(t *testing.T) {
	fixture := newRuleFixture(t)
	ctx := context.Background()
	policyID := fixture.mustSavePolicy(t, []model.QualityRule{{
		ID: "r", Name: "x", MatchType: model.RuleMatchContains, Expression: "x",
		Field: "answer", Severity: model.RuleSeverityInfo, SuggestedAction: model.RuleActionSuggestQuarantine,
	}})

	_, err := fixture.rules.PreviewRules(ctx, fixture.projectID, policyID,
		append(fixture.versionIDs(), 1<<62), 100)
	if !IsStoreValidationError(err) {
		t.Fatalf("含不存在版本必须被拒绝，实际 %v", err)
	}
	// 空范围同样拒绝：否则会「扫描 0 个但显示无命中」，看起来像通过。
	if _, err := fixture.rules.PreviewRules(ctx, fixture.projectID, policyID, nil, 100); !IsStoreValidationError(err) {
		t.Fatalf("空范围必须被拒绝，实际 %v", err)
	}
}

// TestRuleEvidenceKeepsSnapshotAfterRuleChanges 覆盖验收项
// 「历史命中保留原表达式、字段和位置」与「规则删除只影响下一版本」。
func TestRuleEvidenceKeepsSnapshotAfterRuleChanges(t *testing.T) {
	fixture := newRuleFixture(t)
	ctx := context.Background()
	original := model.QualityRule{
		ID: "refusal", Name: "拒答模板", MatchType: model.RuleMatchContains,
		Expression: "抱歉，我不能回答", Field: "answer",
		Severity: model.RuleSeverityError, SuggestedAction: model.RuleActionReview,
	}
	policyID := fixture.mustSavePolicy(t, []model.QualityRule{original})

	preview, err := fixture.rules.PreviewRules(ctx, fixture.projectID, policyID, fixture.versionIDs(), 100)
	if err != nil {
		t.Fatalf("PreviewRules: %v", err)
	}
	if len(preview.Hits) == 0 {
		t.Fatal("fixture 应至少命中一处")
	}

	// 把命中冻结成证据。
	evidence := []model.RuleEvidence{}
	for _, hit := range preview.Hits {
		versionID := int64(0)
		for _, version := range fixture.versions {
			// 命中来自哪一条内容：用片段反查（测试简化），命中不上的用第一条。
			if strings.Contains(string(version.Payload), "抱歉") {
				versionID = version.ID
			}
		}
		evidence = append(evidence, model.RuleEvidenceFromHit(original, hit, 0,
			fixture.projectID, 0, versionID, "hash"))
	}
	evaluationID, err := fixture.rules.RecordRuleEvaluation(ctx, RecordRuleEvaluationInput{
		ProjectID: fixture.projectID, QualityPolicyVersionID: policyID,
		CreatedBy: &fixture.userID, Evidence: evidence, ScannedCount: preview.ScannedCount,
	})
	if err != nil {
		t.Fatalf("RecordRuleEvaluation: %v", err)
	}
	if evaluationID <= 0 {
		t.Fatal("必须返回 evaluationID（否则命中无法归属到某次动作）")
	}

	// 之后把规则**改成完全不同的表达式**（模拟用户修误报），
	// 再保存为新版本（规则删除/修改只影响下一版本）。
	changed := original
	changed.Expression = "全新表达式"
	changed.Field = "question"
	fixture.mustSavePolicy(t, []model.QualityRule{changed})

	// 历史证据必须仍是**当时**的表达式、字段与位置。
	stored, err := fixture.rules.ListRuleEvidence(ctx, fixture.projectID, evidence[0].SampleVersionID, 50)
	if err != nil {
		t.Fatalf("ListRuleEvidence: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("证据必须可读（规则被改后仍保留）")
	}
	if stored[0].RuleExpression != "抱歉，我不能回答" {
		t.Fatalf("历史证据必须保留**当时**的表达式，实际 %q", stored[0].RuleExpression)
	}
	if stored[0].FieldName != "answer" {
		t.Fatalf("历史证据必须保留**当时**的字段，实际 %q", stored[0].FieldName)
	}
	if stored[0].MatchEnd != evidence[0].MatchEnd {
		t.Fatalf("历史证据必须保留命中位置，实际 %d vs %d", stored[0].MatchEnd, evidence[0].MatchEnd)
	}
	if stored[0].Severity != model.RuleSeverityError || stored[0].SuggestedAction != model.RuleActionReview {
		t.Fatalf("历史证据必须保留当时的严重度与建议动作，实际 %+v", stored[0])
	}
}

// TestInvalidRegexIsRejectedAtSaveTime 覆盖验收项「非法 regex 不落库」。
//
// 两道防线都在这里被断言：
//  1. `model.ValidateRuleSpec`（T15）—— 校验即编译，编译不过直接拒绝；
//  2. `SaveVersion`（T04 的 typed 校验）—— 即使绕过第一道，保存阶段仍会拒绝，
//     因此非法正则**根本进不了库**，也就不会在之后每次扫描时运行期失败。
func TestInvalidRegexIsRejectedAtSaveTime(t *testing.T) {
	invalid := model.QualityRule{
		ID: "bad", Name: "坏正则", MatchType: model.RuleMatchRegex,
		Expression: "([", Field: "answer",
		Severity: model.RuleSeverityError, SuggestedAction: model.RuleActionReview,
	}

	// 第一道防线：字段级校验。
	validationErr := model.ValidateRuleSpec(invalid)
	if validationErr == nil {
		t.Fatal("非法正则必须在字段校验阶段被拒绝")
	}
	fieldErrors, ok := model.HasFieldErrors(validationErr)
	if !ok || len(fieldErrors) == 0 {
		t.Fatalf("必须是字段级错误（界面据此聚焦字段），实际 %v", validationErr)
	}
	if strings.Contains(fieldErrors[0].Message, "error parsing") ||
		strings.Contains(fieldErrors[0].Message, "missing closing") {
		t.Fatalf("不得透出 Go 的编译错误原文（含内部语法细节）：%q", fieldErrors[0].Message)
	}

	// 第二道防线：保存阶段。拒绝意味着库里不会出现这条规则。
	fixture := newRuleFixture(t)
	before := ruleCounts(t, fixture)
	if _, err := fixture.savePolicyWithRules(t, []model.QualityRule{invalid}); err == nil {
		t.Fatal("非法正则必须在保存阶段被拒绝（否则每次扫描都会在运行期失败）")
	}
	after := ruleCounts(t, fixture)
	for name, beforeCount := range before {
		if after[name] != beforeCount {
			t.Fatalf("被拒的保存不得写入任何数据：%s 从 %d 变成 %d", name, beforeCount, after[name])
		}
	}
}

// TestPreviewRulesHandlesEmptyFieldCheck 覆盖「字段为空」这类检查。
//
// 「必须非空」的规则在字段为空时**应当命中**（空字段正是它要找的问题），
// 而关键词类规则对空内容没有可命中的位置 —— 两者不能混为一谈。
func TestPreviewRulesHandlesEmptyFieldCheck(t *testing.T) {
	fixture := newRuleFixture(t)
	ctx := context.Background()
	policyID := fixture.mustSavePolicy(t, []model.QualityRule{{
		ID: "non-empty", Name: "答案必须非空", MatchType: model.RuleMatchFieldCheck,
		Expression: "len>0", Field: "answer",
		Severity: model.RuleSeverityError, SuggestedAction: model.RuleActionReview,
	}})

	// 合法字段检查必须被接受（否则「答案非空」这类规则无法保存）。
	if err := model.ValidateRuleSpec(model.QualityRule{
		ID: "non-empty", Name: "x", MatchType: model.RuleMatchFieldCheck,
		Expression: "len>0", Field: "answer",
		Severity: model.RuleSeverityError, SuggestedAction: model.RuleActionReview,
	}); err != nil {
		t.Fatalf("合法字段检查不应报错：%v", err)
	}

	result, err := fixture.rules.PreviewRules(ctx, fixture.projectID, policyID, fixture.versionIDs(), 100)
	if err != nil {
		t.Fatalf("PreviewRules: %v", err)
	}
	// 三条内容里恰好一条 answer 为空。
	if len(result.Hits) != 1 {
		t.Fatalf("只有空字段的那一条应命中，实际 %d：%+v", len(result.Hits), result.Hits)
	}
	if !strings.Contains(result.Hits[0].Snippet, "为空") {
		t.Fatalf("空字段命中必须说明原因，实际 %q", result.Hits[0].Snippet)
	}
}
