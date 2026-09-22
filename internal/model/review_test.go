package model

import (
	"testing"
)

// 本文件验证 Issue #160 T16 的**判定逻辑**（纯函数，不需要数据库）。
//
// T16 最容易写反的正是这里：冲突判定、revision 语义、证据版本参与有效性。
// 写反不会报错，只会静默地「让某个人的意见消失」或「让新旧证据混用」。

func decision(id, reviewerID, reviewerRevision int64, action string, evidenceRevision int64) ReviewDecision {
	return ReviewDecision{
		ID: id, ReviewerID: reviewerID, ReviewerRevision: reviewerRevision,
		Action: action, EvidenceRevision: evidenceRevision, Reason: "理由",
	}
}

// TestComputeProjectionFlagsOppositeDecisionsAsConflict 覆盖验收项
// 「不同人对同一证据版本可以各自提交，意见相反保留两条、标 conflict」。
//
// 关键：**不做多数表决**。2 接纳 / 1 隔离时按多数放行会让少数派的理由
// 被静默忽略，而「少数派为什么说它有问题」往往才是真正要看的东西。
func TestComputeProjectionFlagsOppositeDecisionsAsConflict(t *testing.T) {
	facts := DecisionFacts{
		EvidenceRevision: 1,
		Decisions: []ReviewDecision{
			decision(1, 10, 1, DecisionAccept, 1),
			decision(2, 11, 1, DecisionQuarantine, 1),
		},
	}
	action, reason, conflict := ComputeProjection(facts)
	if action != EffectiveConflict || !conflict {
		t.Fatalf("相反判断必须标 conflict，实际 action=%s conflict=%v", action, conflict)
	}
	if reason != PendingReasonConflict {
		t.Fatalf("待判断原因应为 conflict，实际 %s", reason)
	}

	// 多数派一致也不能压过少数派：2 接纳 + 1 隔离仍是冲突。
	facts.Decisions = append(facts.Decisions, decision(3, 12, 1, DecisionAccept, 1))
	if action, _, conflict := ComputeProjection(facts); action != EffectiveConflict || !conflict {
		t.Fatalf("多数派一致也不得压过少数派，实际 action=%s conflict=%v", action, conflict)
	}
}

// TestComputeProjectionAgreesWhenAllSame 覆盖一致时的有效处置。
func TestComputeProjectionAgreesWhenAllSame(t *testing.T) {
	accept := []ReviewDecision{
		decision(1, 10, 1, DecisionAccept, 1),
		decision(2, 11, 1, DecisionAccept, 1),
	}
	if action, reason, conflict := ComputeProjection(DecisionFacts{
		EvidenceRevision: 1, Decisions: accept,
	}); action != EffectiveAccepted || conflict || reason != "" {
		t.Fatalf("一致接纳应为 accepted，实际 %s/%q/%v", action, reason, conflict)
	}

	quarantine := []ReviewDecision{decision(1, 10, 1, DecisionQuarantine, 1)}
	if action, _, _ := ComputeProjection(DecisionFacts{
		EvidenceRevision: 1, Decisions: quarantine,
	}); action != EffectiveQuarantined {
		t.Fatalf("隔离应为 quarantined，实际 %s", action)
	}
}

// TestComputeProjectionRequiresFreshEvidence 覆盖验收项
// 「新风险不能沿用旧接纳发布」。
//
// 判断确认的证据版本与当前不一致时，即使动作相同也不算有效接纳。
func TestComputeProjectionRequiresFreshEvidence(t *testing.T) {
	stale := []ReviewDecision{decision(1, 10, 1, DecisionAccept, 1)}
	// 当前证据版本已递增到 2，而判断确认的是 1。
	action, reason, _ := ComputeProjection(DecisionFacts{EvidenceRevision: 2, Decisions: stale})
	if action != EffectivePending {
		t.Fatalf("旧证据上的接纳不得继续生效，实际 %s", action)
	}
	if reason != PendingReasonEvidenceChanged {
		t.Fatalf("原因应为 evidence_changed，实际 %s", reason)
	}

	// 基于新证据重新判断后恢复有效。
	fresh := []ReviewDecision{
		decision(1, 10, 1, DecisionAccept, 1),
		decision(2, 10, 2, DecisionAccept, 2),
	}
	if action, _, _ := ComputeProjection(DecisionFacts{EvidenceRevision: 2, Decisions: fresh}); action != EffectiveAccepted {
		t.Fatalf("基于新证据的判断应生效，实际 %s", action)
	}
}

// TestComputeProjectionRequiresFreshContentVersion 覆盖验收项
// 「新内容版本同样需要新判断」。
func TestComputeProjectionRequiresFreshContentVersion(t *testing.T) {
	facts := DecisionFacts{
		EvidenceRevision:     1,
		Decisions:            []ReviewDecision{decision(1, 10, 1, DecisionAccept, 1)},
		ContentHash:          "hash-v2",
		ProjectedContentHash: "hash-v1",
	}
	action, reason, _ := ComputeProjection(facts)
	if action != EffectivePending || reason != PendingReasonNewVersion {
		t.Fatalf("内容新版本必须回到待判断，实际 %s/%s", action, reason)
	}
}

// TestEffectiveDecisionsExcludesSuperseded 覆盖「纠错后旧证据、操作者可追溯」。
func TestEffectiveDecisionsExcludesSuperseded(t *testing.T) {
	supersedes := int64(1)
	all := []ReviewDecision{
		decision(1, 10, 1, DecisionQuarantine, 1),
		{ID: 2, ReviewerID: 10, ReviewerRevision: 2, Action: DecisionAccept,
			EvidenceRevision: 1, Reason: "原判断是误报", Supersedes: &supersedes},
	}
	effective := EffectiveDecisions(all)
	if len(effective) != 1 || effective[0].ID != 2 {
		t.Fatalf("被取代的判断不得参与投影，实际 %+v", effective)
	}
	// 原始判断仍在输入里（调用方按需展示），只是不生效 —— 这是可追溯的前提。
	if len(all) != 2 {
		t.Fatal("原始判断必须保留（只追加）")
	}
}

// TestNextReviewerRevisionIsPerReviewer 覆盖「个人序号」的作用域。
//
// 同一人的序号独立递增；另一个人的序号不受影响 ——
// 这正是「同一人并发更正」与「两人意见相反」能区分开的原因。
func TestNextReviewerRevisionIsPerReviewer(t *testing.T) {
	existing := []ReviewDecision{
		decision(1, 10, 1, DecisionAccept, 1),
		decision(2, 10, 2, DecisionAccept, 1),
		decision(3, 11, 1, DecisionQuarantine, 1),
	}
	if next := NextReviewerRevision(existing, 10); next != 3 {
		t.Fatalf("审阅者 10 的下一个序号应为 3，实际 %d", next)
	}
	if next := NextReviewerRevision(existing, 11); next != 2 {
		t.Fatalf("审阅者 11 的下一个序号应为 2（不受他人影响），实际 %d", next)
	}
	if next := NextReviewerRevision(existing, 12); next != 1 {
		t.Fatalf("新审阅者的第一个序号应为 1，实际 %d", next)
	}
}

// TestValidateSubmitDecisionRequiresReasonAndRevision 覆盖
// 「理由必填」与「必须提供并发序号」。
func TestValidateSubmitDecisionRequiresReasonAndRevision(t *testing.T) {
	valid := SubmitDecisionInput{
		SampleVersionID: 5, EvidenceRevision: 1, ReviewerRevision: 1,
		Action: DecisionAccept, Reason: "推理链完整",
	}
	if err := ValidateSubmitDecision(valid); err != nil {
		t.Fatalf("合法提交不应报错：%v", err)
	}

	cases := []struct {
		name   string
		mutate func(input *SubmitDecisionInput)
	}{
		{"缺理由", func(i *SubmitDecisionInput) { i.Reason = "   " }},
		{"动作非法", func(i *SubmitDecisionInput) { i.Action = "maybe" }},
		{"缺并发序号", func(i *SubmitDecisionInput) { i.ReviewerRevision = 0 }},
		{"缺内容版本", func(i *SubmitDecisionInput) { i.SampleVersionID = 0 }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := valid
			testCase.mutate(&input)
			err := ValidateSubmitDecision(input)
			if err == nil {
				t.Fatal("非法提交必须被拒绝")
			}
			if _, ok := HasFieldErrors(err); !ok {
				t.Fatalf("必须是字段级错误（界面据此聚焦字段），实际 %v", err)
			}
		})
	}

	// 序号从 1 开始：0 与「未提供」无法区分，把未提供当合法会让并发检查静默失效。
	if err := ValidateSubmitDecision(SubmitDecisionInput{
		SampleVersionID: 5, EvidenceRevision: 1, ReviewerRevision: 0,
		Action: DecisionAccept, Reason: "理由",
	}); err == nil {
		t.Fatal("缺并发序号必须被拒绝")
	}
}

// TestReviewProjectionCapabilitiesOnlyOwnerResolvesConflict 覆盖
// 「项目 owner 追加协调决定后才能解除发布阻塞」的权限面。
func TestReviewProjectionCapabilitiesOnlyOwnerResolvesConflict(t *testing.T) {
	owner := ReviewProjectionCapabilities(ProjectRoleOwner, EffectiveConflict)
	if owner.CanPublish {
		t.Fatal("冲突未协调时 owner 也不得发布（阻塞必须真实生效）")
	}
	reviewer := ReviewProjectionCapabilities(ProjectRoleReviewer, EffectiveConflict)
	if reviewer.CanEdit {
		t.Fatal("reviewer 不得拥有协调编辑能力（协调是 owner 的决定）")
	}
	if !reviewer.CanReview {
		t.Fatal("reviewer 必须能继续表达意见")
	}
	viewer := ReviewProjectionCapabilities(ProjectRoleViewer, EffectiveAccepted)
	if viewer.CanReview || viewer.CanEdit {
		t.Fatal("viewer 只读")
	}
	// 已接纳时 owner 可以发布。
	if !ReviewProjectionCapabilities(ProjectRoleOwner, EffectiveAccepted).CanPublish {
		t.Fatal("已接纳时 owner 应能发布")
	}
	// 隔离不由 owner 的发布能力绕过。
	if ReviewProjectionCapabilities(ProjectRoleOwner, EffectiveQuarantined).CanPublish {
		t.Fatal("隔离的处置不应因为角色而改变")
	}
}
