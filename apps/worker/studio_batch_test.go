package main

import (
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件验证 T12 里「新契约 StandardStep → 旧生成器 ChainStep」的适配。
//
// 为什么这段适配值得一个测试：它是新旧契约的**唯一接缝**，而顺序错误
// 不会报任何错 —— 生成模型会按错误的步骤顺序推理，产出看起来正常
// 但思维链与标准不符的内容。那类缺陷只能从测试或人工审阅发现。

// TestToChainStepsOrdersByExplicitOrder 覆盖「新 schema 的步骤顺序是数据」。
func TestToChainStepsOrdersByExplicitOrder(t *testing.T) {
	steps := []model.StandardStep{
		{ID: "c", Title: "第三步", Order: 3},
		{ID: "a", Title: "第一步", Order: 1},
		{ID: "b", Title: "第二步", Order: 2},
	}
	converted := toChainSteps(steps)
	if len(converted) != 3 {
		t.Fatalf("应转换 3 步，实际 %d", len(converted))
	}
	wantTitles := []string{"第一步", "第二步", "第三步"}
	for index, want := range wantTitles {
		if converted[index].Title != want {
			t.Fatalf("第 %d 步应为 %q，实际 %q（必须按 order 排序，而不是数组顺序）",
				index+1, want, converted[index].Title)
		}
		// Index 从 1 开始：旧类型用它做展示编号，从 0 开始会生成「第 0 步」。
		if converted[index].Index != index+1 {
			t.Fatalf("第 %d 步的 Index 应为 %d，实际 %d", index+1, index+1, converted[index].Index)
		}
	}
}

// TestToChainStepsKeepsOriginalOrderWhenOrderMissing 覆盖「order 都为 0 时保持原顺序」。
//
// 稳定排序很重要：用户在界面上排好顺序但没填 order 字段时，
// 不能因为排序把顺序打乱 —— 那会让「界面显示的步骤顺序」与
// 「模型收到的步骤顺序」不一致。
func TestToChainStepsKeepsOriginalOrderWhenOrderMissing(t *testing.T) {
	steps := []model.StandardStep{
		{ID: "1", Title: "甲"},
		{ID: "2", Title: "乙"},
		{ID: "3", Title: "丙"},
	}
	converted := toChainSteps(steps)
	for index, want := range []string{"甲", "乙", "丙"} {
		if converted[index].Title != want {
			t.Fatalf("order 缺失时必须保持原顺序：第 %d 步应为 %q，实际 %q",
				index+1, want, converted[index].Title)
		}
	}
}

// TestToChainStepsMapsDetailToDescription 覆盖字段映射。
//
// 两个类型的字段名不同（Detail vs Description），映射写错会让提示词里
// 的步骤描述变成空串 —— 模型仍然会产出内容，因此不会报错。
func TestToChainStepsMapsDetailToDescription(t *testing.T) {
	converted := toChainSteps([]model.StandardStep{
		{ID: "a", Title: "识别约束", Detail: "列出所有显式与隐式约束", Checkpoint: "约束清单完整"},
	})
	if len(converted) != 1 {
		t.Fatalf("应转换 1 步，实际 %d", len(converted))
	}
	if converted[0].Description != "列出所有显式与隐式约束" {
		t.Fatalf("Detail 必须映射到 Description，实际 %q", converted[0].Description)
	}
	if converted[0].Checkpoint != "约束清单完整" {
		t.Fatalf("Checkpoint 必须原样保留，实际 %q", converted[0].Checkpoint)
	}
	if got := toChainSteps(nil); len(got) != 0 {
		t.Fatalf("空输入应得到空结果，实际 %d", len(got))
	}
}
