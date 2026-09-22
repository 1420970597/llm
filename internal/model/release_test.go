package model

import (
	"strings"
	"testing"
)

// 本文件验证 Issue #160 T20 的发布门槛判定（纯函数）。
//
// 门槛的错法都很安静：漏掉一条阻塞会发布出带问题的数据，
// 而多算一条阻塞会让「本该能发布」的版本卡住。两者都不会报错。

func acceptedItem(versionID int64) ReleaseItemFacts {
	return ReleaseItemFacts{
		SampleID: versionID, SampleVersionID: versionID,
		EffectiveAction:         EffectiveAccepted,
		AggregateReviewRevision: 3, CurrentAggregateReviewRevision: 3,
		EvidenceRevision: 2, CurrentEvidenceRevision: 2,
	}
}

func validGateInput() ReleaseGateInput {
	return ReleaseGateInput{
		ReleaseName: "v1.0", MappingVersionID: 7, Format: "jsonl", IntendedUse: "SFT 训练",
		Items: []ReleaseItemFacts{acceptedItem(1), acceptedItem(2)},
	}
}

// blockersByCode 收集某个代码的阻塞项数量。
func blockersByCode(result ReleaseGateResult, code string) int {
	count := 0
	for _, blocker := range result.Blockers {
		if blocker.Code == code {
			count++
		}
	}
	return count
}

// TestReleaseGatePassesOnCleanCandidate 覆盖「干净的候选通过」。
func TestReleaseGatePassesOnCleanCandidate(t *testing.T) {
	result := EvaluateReleaseGate(validGateInput())
	if !result.Passed || len(result.Blockers) != 0 {
		t.Fatalf("干净候选必须通过，实际 passed=%v blockers=%+v", result.Passed, result.Blockers)
	}
	if result.Inspected != 2 {
		t.Fatalf("分母应为 2，实际 %d", result.Inspected)
	}
	if result.AcceptanceRate == nil || *result.AcceptanceRate != 1 {
		t.Fatalf("全部接纳时接纳率应为 1，实际 %v", result.AcceptanceRate)
	}
}

// TestReleaseGateRequiresAllFieldsAndReportsEveryProblem 覆盖
// 「必需字段」与「一次报全部」（后者与后端 Validate 同一约定）。
func TestReleaseGateRequiresAllFieldsAndReportsEveryProblem(t *testing.T) {
	input := validGateInput()
	input.ReleaseName = "  "
	input.MappingVersionID = 0
	input.Format = ""
	input.IntendedUse = ""
	result := EvaluateReleaseGate(input)

	if result.Passed {
		t.Fatal("缺必需字段必须阻塞")
	}
	if got := blockersByCode(result, BlockerMissingField); got != 4 {
		t.Fatalf("4 个缺失字段都应各报一条（用户一次修完比来回提交四次快），实际 %d", got)
	}
	// 字段级阻塞必须带 Field，界面据此聚焦输入框。
	for _, blocker := range result.Blockers {
		if blocker.Code == BlockerMissingField && blocker.Field == "" {
			t.Fatalf("字段级阻塞必须带 Field 以便聚焦，实际 %+v", blocker)
		}
	}
}

// TestReleaseGateBlocksPendingConflictAndQuarantine 覆盖
// 「待审阅与冲突为零」与「隔离项不得进入发布范围」。
func TestReleaseGateBlocksPendingConflictAndQuarantine(t *testing.T) {
	pending := ReleaseItemFacts{SampleVersionID: 1, EffectiveAction: EffectivePending}
	conflict := ReleaseItemFacts{SampleVersionID: 2, EffectiveAction: EffectiveConflict}
	quarantined := ReleaseItemFacts{SampleVersionID: 3, EffectiveAction: EffectiveQuarantined}

	input := validGateInput()
	input.Items = []ReleaseItemFacts{acceptedItem(9), pending, conflict, quarantined}
	result := EvaluateReleaseGate(input)

	if result.Passed {
		t.Fatal("有待审阅/冲突/隔离时不得通过")
	}
	if blockersByCode(result, BlockerPendingReview) != 1 {
		t.Fatalf("应有 1 条待审阅阻塞，实际 %+v", result.Blockers)
	}
	if blockersByCode(result, BlockerReviewConflict) != 1 {
		t.Fatalf("应有 1 条冲突阻塞，实际 %+v", result.Blockers)
	}
	if blockersByCode(result, BlockerQuarantined) != 1 {
		t.Fatalf("应有 1 条隔离阻塞，实际 %+v", result.Blockers)
	}
	// 每条阻塞必须指向**具体内容版本**（否则用户要在几万条里自己找）。
	for _, blocker := range result.Blockers {
		if blocker.SampleVersionID == nil {
			t.Fatalf("逐项阻塞必须带 sampleVersionId，实际 %+v", blocker)
		}
		if blocker.Message == "" {
			t.Fatalf("阻塞必须带可读原因，实际 %+v", blocker)
		}
	}
}

// TestReleaseGateDetectsConcurrentRevisionChange 覆盖验收项
// 「并发隔离/更正/新增证据不会绕过门槛」。
//
// 这是本文件最重要的一条：确认与冻结之间有人改了判断或证据时，
// 必须要求**重新确认**，而不是用旧确认放行。
func TestReleaseGateDetectsConcurrentRevisionChange(t *testing.T) {
	// 判断被改过（aggregate revision 变了）。
	stale := acceptedItem(1)
	stale.CurrentAggregateReviewRevision = stale.AggregateReviewRevision + 1

	input := validGateInput()
	input.Items = []ReleaseItemFacts{stale}
	result := EvaluateReleaseGate(input)
	if result.Passed {
		t.Fatal("判断在确认后变化时必须阻塞并重新确认")
	}
	if blockersByCode(result, BlockerEvidenceIncomplete) != 1 {
		t.Fatalf("应报证据/判断变化阻塞，实际 %+v", result.Blockers)
	}

	// 证据集被递增（发现新风险）。
	staleEvidence := acceptedItem(2)
	staleEvidence.CurrentEvidenceRevision = staleEvidence.EvidenceRevision + 1
	input.Items = []ReleaseItemFacts{staleEvidence}
	result = EvaluateReleaseGate(input)
	if blockersByCode(result, BlockerEvidenceIncomplete) != 1 {
		t.Fatalf("证据集变化必须阻塞，实际 %+v", result.Blockers)
	}
}

// TestReleaseGateRequiresExclusionReason 覆盖验收项
// 「隔离排除有原因」。
func TestReleaseGateRequiresExclusionReason(t *testing.T) {
	excluded := acceptedItem(1)
	excluded.Excluded = true // 排除但没写原因
	excluded.EffectiveAction = EffectiveQuarantined

	input := validGateInput()
	input.Items = []ReleaseItemFacts{acceptedItem(2), excluded}
	result := EvaluateReleaseGate(input)
	if result.Passed {
		t.Fatal("无原因的排除必须阻塞（数据卡无法解释覆盖损失）")
	}
	if blockersByCode(result, BlockerExclusionNoReason) != 1 {
		t.Fatalf("应报排除无原因，实际 %+v", result.Blockers)
	}

	// 写了原因后通过（隔离项被排除是**正当**做法）。
	explained := excluded
	explained.ExcludedReason = "规则命中为真阳性，已隔离"
	input.Items = []ReleaseItemFacts{acceptedItem(2), explained}
	if result := EvaluateReleaseGate(input); !result.Passed {
		t.Fatalf("有原因的排除不应阻塞，实际 %+v", result.Blockers)
	}
}

// TestReleaseGateKeepsDenominatorWhenItemsExcluded 覆盖 §2.3
// 「发布范围可缩小，但数据卡同时保留原范围指标与覆盖损失」。
//
// 这是「分母不可通过过滤优化」在发布侧的体现：排除几条差的**不能**提高接纳率。
func TestReleaseGateKeepsDenominatorWhenItemsExcluded(t *testing.T) {
	// 4 条：1 接纳、3 隔离。若把 3 条隔离排除掉，分母仍是 4 → 接纳率 25%。
	excludedQuarantined := func(versionID int64) ReleaseItemFacts {
		item := acceptedItem(versionID)
		item.EffectiveAction = EffectiveQuarantined
		item.Excluded = true
		item.ExcludedReason = "规则命中为真阳性"
		return item
	}
	input := validGateInput()
	input.Items = []ReleaseItemFacts{
		acceptedItem(1), excludedQuarantined(2), excludedQuarantined(3), excludedQuarantined(4),
	}
	input.AcceptanceRateTarget = 0.5 // 目标 50%
	result := EvaluateReleaseGate(input)

	if result.Inspected != 4 {
		t.Fatalf("分母必须仍是原范围 4（排除不缩小分母），实际 %d", result.Inspected)
	}
	if result.AcceptanceRate == nil || *result.AcceptanceRate != 0.25 {
		t.Fatalf("接纳率应为 1/4=0.25，实际 %v（排除差样本不能提高接纳率）", result.AcceptanceRate)
	}
	if blockersByCode(result, BlockerQualityTargetMissed) != 1 {
		t.Fatalf("未达目标必须阻塞，实际 %+v", result.Blockers)
	}
	if !strings.Contains(result.Blockers[len(result.Blockers)-1].Message, "冻结的 4 个内容版本") {
		t.Fatalf("质量阻塞必须说明分母来自冻结范围，实际 %q", result.Blockers[len(result.Blockers)-1].Message)
	}
}

// TestReleaseGateBlocksEmptyRange 覆盖「空范围阻塞」。
//
// 空范围会产出「0 条通过」这种看起来成功的结果。
func TestReleaseGateBlocksEmptyRange(t *testing.T) {
	input := validGateInput()
	input.Items = nil
	result := EvaluateReleaseGate(input)
	if result.Passed {
		t.Fatal("空范围必须阻塞")
	}
	if blockersByCode(result, BlockerEmptyRange) != 1 {
		t.Fatalf("应报空范围，实际 %+v", result.Blockers)
	}
	if result.AcceptanceRate != nil {
		t.Fatalf("空范围的接纳率必须是 nil（无结论），实际 %v", *result.AcceptanceRate)
	}
	if result.AcceptanceRateDisplay != "无结论" {
		t.Fatalf("空范围应显示「无结论」而不是 100%%，实际 %q", result.AcceptanceRateDisplay)
	}
}

// TestValidateReleaseNameRejectsLatestAndPathCharacters 覆盖版本名约束。
//
// 禁止 `latest`：下载路径禁止 latest 回退，而一个叫 latest 的版本名
// 会让用户以为文件总是最新，实际它是固定的一版。
func TestValidateReleaseNameRejectsLatestAndPathCharacters(t *testing.T) {
	if err := ValidateReleaseName("v1.2"); err != nil {
		t.Fatalf("合法版本名不应报错：%v", err)
	}
	for _, invalid := range []string{"", "   ", "latest", "LATEST", "a/b", "a\\b", "a:b", strings.Repeat("x", 65)} {
		if err := ValidateReleaseName(invalid); err == nil {
			t.Fatalf("非法版本名 %q 必须被拒绝", invalid)
		}
	}
	// 版本名的**规范化键**用于唯一约束：大小写差异不得让两版同时存在。
	if NormalizeReleaseNameKey(" V1.2 ") != NormalizeReleaseNameKey("v1.2") {
		t.Fatal("规范化键必须忽略大小写与空白（否则 V1.2 与 v1.2 会同时存在）")
	}
}

// TestReleaseStatusTerminalAndCapabilities 覆盖「发布后只读」。
func TestReleaseStatusTerminalAndCapabilities(t *testing.T) {
	if !ReleaseStatusTerminal(ReleaseStatusPublished) {
		t.Fatal("published 必须是终态（发布后只读）")
	}
	// build_failed **不是**终态：它必须能幂等续接（§2.9「失败为 build_failed，可幂等续接」）。
	if ReleaseStatusTerminal(ReleaseStatusBuildFailed) {
		t.Fatal("build_failed 不得是终态：发布失败必须能幂等续接")
	}
	if ReleaseStatusTerminal("some-unknown-status") {
		t.Fatal("未知状态不得被当成终态")
	}
	published := ReleaseCapabilities(ProjectRoleOwner, ReleaseStatusPublished)
	if published.CanPublish || published.CanEdit {
		t.Fatal("published 之后不得再发布或编辑（后续风险用独立警告表达）")
	}
	if !published.CanDownload {
		t.Fatal("published 必须可下载")
	}
	building := ReleaseCapabilities(ProjectRoleOwner, ReleaseStatusBuilding)
	if building.CanPublish {
		t.Fatal("building 期间不得重复发布（重复命令返回同一个 release）")
	}
	blocked := ReleaseCapabilities(ProjectRoleOwner, ReleaseStatusBlocked)
	if !blocked.CanPublish {
		t.Fatal("blocked 是「不能发布」，但 owner 修完问题后应能重新发布")
	}
	if ReleaseCapabilities(ProjectRoleReviewer, ReleaseStatusCandidate).CanPublish {
		t.Fatal("reviewer 不得发布")
	}
}
