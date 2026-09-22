package studio

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// TestReviewBlockersCarryActionableNextStep 覆盖验收项
// 「每条 blocker 带可跳转对象」（契约 §2.8）。
//
// 阻塞项是 T20 发布门槛的直接输入，因此它必须能点到**具体对象**：
// 一句「还有待审阅」会让用户自己去列表里找是哪几条。
func TestReviewBlockersCarryActionableNextStep(t *testing.T) {
	ref := SampleVersionRef{ProjectID: 7, SampleID: 12, Version: 3}
	if !strings.Contains(ref.Link(), "/api/v1/projects/7/samples/s_12/versions/3") {
		t.Fatalf("blocker 必须指向具体内容版本，实际 %s", ref.Link())
	}

	cases := []struct {
		name       string
		projection model.ReviewProjection
		wantCode   string
		wantText   string
	}{
		{"未判断", model.ReviewProjection{EffectiveAction: model.EffectivePending,
			PendingReason: model.PendingReasonNoDecision}, "PENDING_REVIEW", "尚未"},
		{"证据变化", model.ReviewProjection{EffectiveAction: model.EffectivePending,
			PendingReason: model.PendingReasonEvidenceChanged}, "PENDING_REVIEW", "证据集已变化"},
		{"新版本", model.ReviewProjection{EffectiveAction: model.EffectivePending,
			PendingReason: model.PendingReasonNewVersion}, "PENDING_REVIEW", "新版本"},
		{"冲突", model.ReviewProjection{EffectiveAction: model.EffectiveConflict}, "REVIEW_CONFLICT", "协调"},
		{"隔离", model.ReviewProjection{EffectiveAction: model.EffectiveQuarantined}, "QUARANTINED", "隔离"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			blockers := ReviewBlockers(ref, testCase.projection)
			if len(blockers) != 1 {
				t.Fatalf("应恰好一条阻塞项，实际 %d", len(blockers))
			}
			if blockers[0].Code != testCase.wantCode {
				t.Fatalf("阻塞码应为 %s，实际 %s", testCase.wantCode, blockers[0].Code)
			}
			if !strings.Contains(blockers[0].Message, testCase.wantText) {
				t.Fatalf("提示必须说明具体原因（含 %q），实际 %q", testCase.wantText, blockers[0].Message)
			}
			if blockers[0].Link == "" {
				t.Fatal("每条阻塞项都必须可跳转")
			}
		})
	}

	// 已接纳且无冲突时不得有阻塞项（否则门槛会把可发布内容永久挡住）。
	if blockers := ReviewBlockers(ref, model.ReviewProjection{EffectiveAction: model.EffectiveAccepted}); len(blockers) != 0 {
		t.Fatalf("已接纳不应产生阻塞项，实际 %+v", blockers)
	}
}
