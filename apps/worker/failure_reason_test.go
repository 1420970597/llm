package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/store"
)

// TestFailureReasonTranslatesStorageErrors 是 issue #83 的核心守卫：
// 存储配置缺失必须翻译成**可操作**的中文提示。
//
// 判据不只是「有中文」，而是提示里必须含**去哪修**（「系统设置」）——
// 这正是原缺陷的本质：用户看到「系统同步中」，不知原因也不知怎么修。
func TestFailureReasonTranslatesStorageErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantSubstr []string
	}{
		{
			name:       "完全没有可用存储配置",
			err:        store.ErrNoStorageProfile,
			wantSubstr: []string{"结果存储", "系统设置"},
		},
		{
			name:       "指定的存储不存在或已停用",
			err:        store.ErrStorageProfileNotFound,
			wantSubstr: []string{"结果存储", "重新选择"},
		},
		{
			// 包装后仍必须被识别：worker 在调用链上会用 fmt.Errorf("%w") 包装。
			name:       "被包装的存储错误仍可识别",
			err:        fmt.Errorf("resolve storage for dataset 6: %w", store.ErrNoStorageProfile),
			wantSubstr: []string{"结果存储", "系统设置"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reason := failureReason(testCase.err)
			if reason == "" {
				t.Fatal("失败原因不能为空")
			}
			for _, want := range testCase.wantSubstr {
				if !strings.Contains(reason, want) {
					t.Errorf("提示应含 %q（用户要能照着做），得到 %q", want, reason)
				}
			}
		})
	}
}

// TestFailureReasonNeverLeaksRawPgxError 确认不会把内部英文错误串
// 原样当成用户提示。
//
// 这条直接对应 issue #83：真实原因是 `no rows in result set`，
// 而它只应该出现在日志里，不应成为界面文案。
func TestFailureReasonNeverLeaksRawPgxError(t *testing.T) {
	reason := failureReason(store.ErrNoStorageProfile)
	for _, forbidden := range []string{"no rows", "result set", "pgx"} {
		if strings.Contains(reason, forbidden) {
			t.Errorf("用户可见文案不应包含内部英文串 %q，得到 %q", forbidden, reason)
		}
	}
}

// TestFailureReasonGenericErrorKeepsBoundedDetail 确认通用错误既有中文结论，
// 也保留了**有上限**的技术摘要。
//
// 为什么两者都要：
//   - 只有中文结论 → 用户/运维无法自助定位（又回到「不知道原因」）；
//   - 只有原始英文 → 对用户没有指导意义；
//   - 不截断 → 一个巨型错误串会把界面撑坏。
func TestFailureReasonGenericErrorKeepsBoundedDetail(t *testing.T) {
	reason := failureReason(errors.New("provider returned no choices"))
	if !strings.Contains(reason, "本阶段执行失败") {
		t.Errorf("通用错误应有中文结论，得到 %q", reason)
	}
	if !strings.Contains(reason, "provider returned no choices") {
		t.Errorf("通用错误应保留技术摘要便于定位，得到 %q", reason)
	}

	long := strings.Repeat("很长的错误内容", 200)
	truncated := failureReason(errors.New(long))
	if runes := []rune(truncated); len(runes) > 400 {
		t.Errorf("超长错误应被截断（<=400 rune），实际 %d rune", len(runes))
	}
	if !strings.Contains(truncated, "…") {
		t.Errorf("截断应有明确标记（…），得到尾部 %q", string([]rune(truncated)[max(0, len([]rune(truncated))-20):]))
	}
}

// TestFailureReasonNilIsEmpty 确认 nil 错误不产出伪原因 ——
// 否则成功路径也会写一条失败提示。
func TestFailureReasonNilIsEmpty(t *testing.T) {
	if reason := failureReason(nil); reason != "" {
		t.Errorf("nil 错误应产出空原因，得到 %q", reason)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
