package model

import "testing"

// TestRecordStatusUsableForDownstream 锁定契约 §1.3 的下游过滤规则。
//
// 规则原文：「只有 generated 可进入导出与评估；invalid 与 failed 同等看待」。
//
// 为什么需要这条测试：生成侧新增状态取值时，消费侧（导出、评估共 4 处 SQL/Go 判断）
// 不会自动跟着变。issue #7 就是这样在出口处失守的 —— content_validator 会写 `invalid`，
// 而下游只判断 `!= "failed"`，于是占位内容照样进训练集。
func TestRecordStatusUsableForDownstream(t *testing.T) {
	cases := []struct {
		status string
		want   bool
		why    string
	}{
		{RecordStatusGenerated, true, "generated 是唯一可用取值"},
		{RecordStatusFailed, false, "failed 不可用（网络/超时/解析失败）"},
		{RecordStatusInvalid, false, "invalid 不可用（占位内容，issue #7 的核心）"},
		{"", false, "空状态视为不可用（未设置不应放行）"},
		{"partial", false, "未知取值默认不可用（白名单语义，防将来新增取值被静默放行）"},
		{"GENERATED", false, "大小写敏感：数据库写的是小写字面量，避免出现两套拼写"},
	}

	for _, tc := range cases {
		got := RecordStatusUsableForDownstream(tc.status)
		if got != tc.want {
			t.Errorf("RecordStatusUsableForDownstream(%q) = %v, 期望 %v（%s）",
				tc.status, got, tc.want, tc.why)
		}
	}
}

// TestRecordStatusConstantsMatchContract 断言取值字面量与契约 §1.3 逐字一致。
//
// 契约冻结的是字面量本身（数据库里存的、迁移里默认的就是这些串），
// 改名会静默让所有历史数据失效，因此必须钉住。
func TestRecordStatusConstantsMatchContract(t *testing.T) {
	want := map[string]string{
		"generated": RecordStatusGenerated,
		"failed":    RecordStatusFailed,
		"invalid":   RecordStatusInvalid,
	}
	for literal, got := range want {
		if got != literal {
			t.Errorf("常量的字面量必须与契约 §1.3 一致：期望 %q，实际 %q", literal, got)
		}
	}
}

// TestLLMInvalidConstantMatchesModel 断言生成侧（internal/llm）与
// 下游判定（internal/model）用的是同一个字面量。
//
// 两侧一旦漂移（例如 llm 写成 "invalid-content"），过滤会静默失效：
// 生成侧写的值不在白名单里 → 记录永远不可用（表现为「数据凭空消失」），
// 或反过来说不清到底哪一侧错了。
func TestLLMInvalidConstantMatchesModel(t *testing.T) {
	if RecordStatusInvalid != "invalid" {
		t.Fatalf("internal/model 的 invalid 字面量必须是 \"invalid\"，实际 %q", RecordStatusInvalid)
	}
	// 这里只断言 model 侧；llm 侧的同值断言在 internal/llm/content_validator_test.go，
	// 两处各自独立钉住契约字面量，避免单点失效。
}
