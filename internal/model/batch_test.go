package model

import (
	"strings"
	"testing"
)

// TestErrorClassLabelNeverLeaksRawCode 覆盖 issue #191 第 2 轮扫描发现的泄漏点：
// 批次失败卡以前直接渲染 `error_class`（实测可见 `config_error`）。
//
// 两条断言：
//  1. 每个**内置**错误类别都必须有中文名（漏一个就会在界面上露出内部码）；
//  2. 未知类别也不得裸回传原始码 —— 与 #191/#206/#211 同一契约。
//
// 第 2 条刻意不用「等于入参」判定：未知类别允许带上可读后缀（排查线索），
// 因此断言的是「整体是中文为主的描述」而不是「不等于原串」。
func TestErrorClassLabelNeverLeaksRawCode(t *testing.T) {
	builtin := []string{
		ErrorClassProvider, ErrorClassRateLimited, ErrorClassTimeout,
		ErrorClassEmptyOutput, ErrorClassTruncated, ErrorClassInvalidJSON,
		ErrorClassSchema, ErrorClassConfig, ErrorClassInternal,
	}
	for _, class := range builtin {
		label := ErrorClassLabel(class)
		if label == "" {
			t.Fatalf("内置错误类别 %q 没有中文名（界面会显示内部码）", class)
		}
		// 内置类别必须是**纯中文描述**：不能把内部码留在文案里。
		if strings.Contains(label, class) {
			t.Fatalf("内置错误类别 %q 的中文名 %q 仍包含内部码", class, label)
		}
		if !containsHan(label) {
			t.Fatalf("内置错误类别 %q 的中文名 %q 不含汉字", class, label)
		}
	}

	// 边界路径：未知类别 / 空串。
	if got := ErrorClassLabel(""); got != "未知错误" {
		t.Fatalf("空错误类别应为「未知错误」，实际 %q", got)
	}
	unknown := ErrorClassLabel("some_new_failure_mode")
	if !strings.HasPrefix(unknown, "其他错误（") {
		t.Fatalf("未知错误类别应是可读的中性文案，实际 %q", unknown)
	}
	// 未知类别允许保留原码作排查线索，但必须**嵌在中文框架里**而不是裸码。
	if unknown == "some_new_failure_mode" {
		t.Fatal("未知错误类别不得裸回传原始码（#191 的缺陷形态）")
	}
}

// containsHan 判断字符串里是否至少有一个汉字。
func containsHan(text string) bool {
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// TestBatchStepLabelsAndStatusFollowFacts 覆盖 issue #212 的阶段投影口径。
//
// 缺陷形态（实测 b_4）：已完成 4/4 的批次详情显示「还没有阶段记录」，
// 用空态宣称「这次没有执行任何阶段」。修法是把阶段进度做成 batch_items 的
// 投影，因此这两条纯函数必须：
//
//  1. 永不把内部阶段键回传成界面文案（#191/#206/#211 的同一契约）；
//  2. 状态能同时表达「全部产出」「部分产出但有缺口」「还在推进」三种事实，
//     且已定稿批次不得显示成「正在推进」。
func TestBatchStepLabelsAndStatusFollowFacts(t *testing.T) {
	// 正常路径：已定稿且产出齐全 → completed。
	if got := StepStatusFor(BatchStatusCompleted, 4, 0, 4); got != StepStatusCompleted {
		t.Fatalf("已完成 4/4 的阶段必须是 completed，实际 %q", got)
	}
	// 边界/异常路径 1：批次已定稿但这一阶段只产出 1/4（b_2 的实测形态）。
	// 显示成 running 会让「已完成」的批次里出现一个永远在跑的阶段。
	if got := StepStatusFor(BatchStatusCompleted, 1, 0, 4); got != StepStatusPartialFailed {
		t.Fatalf("已定稿但产出不足的阶段必须是 partial_failed，实际 %q", got)
	}
	// 边界/异常路径 2：还在推进的批次必须显示 running。
	if got := StepStatusFor(BatchStatusRunning, 1, 0, 4); got != StepStatusRunning {
		t.Fatalf("推进中的阶段必须是 running，实际 %q", got)
	}
	if got := StepStatusFor(BatchStatusQueued, 0, 0, 4); got != StepStatusPending {
		t.Fatalf("尚未开始的排队阶段必须是 pending，实际 %q", got)
	}
	// 边界/异常路径 3：零计划量的空批次没有待办工作，「已完成」才是诚实描述
	//（与 #190 对空批次的判定同一口径）。
	if got := StepStatusFor(BatchStatusCompleted, 0, 0, 0); got != StepStatusCompleted {
		t.Fatalf("零计划量的阶段必须是 completed，实际 %q", got)
	}
	// 边界/异常路径 4：全部定稿但有失败 → partial_failed（失败必须可见）。
	if got := StepStatusFor(BatchStatusPartialFailed, 3, 1, 4); got != StepStatusPartialFailed {
		t.Fatalf("有失败的阶段必须是 partial_failed，实际 %q", got)
	}
	// 边界/异常路径 5：失败为致命终态。
	if got := StepStatusFor(BatchStatusFailed, 0, 2, 2); got != StepStatusPartialFailed {
		t.Fatalf("全部定稿且有失败的阶段是 partial_failed，实际 %q", got)
	}

	// 中文文案：内置阶段必须是纯中文，且不得包含内部键。
	for _, phase := range []string{BatchStepPlan, BatchStepGenerate} {
		label := BatchStepLabel(phase)
		if !containsHan(label) {
			t.Fatalf("阶段 %q 的文案 %q 不含汉字", phase, label)
		}
		if strings.Contains(label, phase) {
			t.Fatalf("阶段 %q 的文案 %q 仍包含内部键", phase, label)
		}
	}
	// 未知阶段不得裸回传原始键（否则中文界面会中英混杂）。
	unknown := BatchStepLabel("some_new_phase")
	if !containsHan(unknown) || unknown == "some_new_phase" {
		t.Fatalf("未知阶段必须给可读中文文案而不是裸键，实际 %q", unknown)
	}
}
