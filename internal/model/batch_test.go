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
