package main

import (
	"strings"
	"testing"
)

// 本文件覆盖 L12 清洗运行请求体 ruleIds 的解析逻辑。
//
// 背景：冻结契约（docs/plans/eval-and-cleaning-plan.md 第 321 行）承诺
// POST /api/v1/datasets/{id}/cleaning/run 接受 { stages, ruleIds }，但 0012
// 起的实现只读了 stages，ruleIds 被静默忽略——用户勾了「本次只用这几条规则」
// 却仍按全部启用规则清洗，且不报错。这类静默失效比直接报错更糟。
//
// 这些测试锁死修复后的语义，且不依赖真实 Postgres（与 apps/api 现有测试一致）。

// 未指定规则 → 空切片，语义是「用全部启用规则」，保持向后兼容。
func TestResolveCleaningRuleIDsEmptyMeansAll(t *testing.T) {
	got, err := resolveCleaningRuleIDs(nil, []int64{1, 2, 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("nil request must yield empty slice (means all active rules), got %+v", got)
	}
	if got == nil {
		t.Fatal("must return empty non-nil slice so JSON renders [] instead of null")
	}
}

// 显式指定 → 原样返回，且不因为规则处于停用状态而被过滤。
// 这是「显式优先于开关」的核心断言。
func TestResolveCleaningRuleIDsKeepsExplicitIDs(t *testing.T) {
	got, err := resolveCleaningRuleIDs([]int64{7, 9}, []int64{7, 9})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != 7 || got[1] != 9 {
		t.Fatalf("got %+v, want [7 9]", got)
	}
}

// 重复 ID 去重，保留首次出现顺序，避免同一规则被重复计入命中统计。
func TestResolveCleaningRuleIDsDeduplicates(t *testing.T) {
	got, err := resolveCleaningRuleIDs([]int64{5, 3, 5, 3, 8}, []int64{3, 5, 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 || got[0] != 5 || got[1] != 3 || got[2] != 8 {
		t.Fatalf("got %+v, want [5 3 8] (dedup, first-seen order)", got)
	}
}

// 未知 ID 必须报错并列出全部未知项，而不是静默丢弃。
func TestResolveCleaningRuleIDsRejectsUnknown(t *testing.T) {
	_, err := resolveCleaningRuleIDs([]int64{1, 404, 405}, []int64{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for unknown rule IDs, got nil (silent drop is the bug being fixed)")
	}
	msg := err.Error()
	for _, want := range []string{"404", "405"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error must list unknown ID %s, got %q", want, msg)
		}
	}
	if strings.Contains(msg, " 1") || strings.HasSuffix(msg, "1") {
		t.Fatalf("error must not list known ID 1 as unknown, got %q", msg)
	}
}

// 全部未知 → 报错，且返回 nil 切片（调用方据此不建 run）。
func TestResolveCleaningRuleIDsAllUnknown(t *testing.T) {
	got, err := resolveCleaningRuleIDs([]int64{99}, []int64{1, 2})
	if err == nil {
		t.Fatal("expected error when every requested ID is unknown")
	}
	if got != nil {
		t.Fatalf("on error the slice must be nil, got %+v", got)
	}
}

// 库中无任何规则时，显式指定必然全部未知 → 报错而非静默清空。
func TestResolveCleaningRuleIDsNoKnownRules(t *testing.T) {
	if _, err := resolveCleaningRuleIDs([]int64{1}, nil); err == nil {
		t.Fatal("expected error when rule table is empty and IDs are requested")
	}
}
