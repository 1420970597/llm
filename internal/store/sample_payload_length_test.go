package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 本文件验证样本版本分析事实里的**长度口径**（issue #197 第 13 条）。
//
// 为什么需要这些断言：`PayloadChars` 是多个文本字段的**合计**，而
// 「合计了哪几个」此前只存在于 `summarizePayload` 的实现细节里 —— 上层只能
// 硬编码一个字段数，界面上也没有任何口径说明。用户看到「长度中位 1082」时
// 无法分辨这是问题+推理+答案之和还是单看问题。
//
// 这里的断言把「长度由哪些字段构成」变成可核对的读数：
//   - 正常路径：三个必填字段都参与，且逐字段字符数之和 == 总字符数；
//   - 边界路径：空串字段 / 非字符串字段 / 非法 JSON 不得被算作参与。
//
// 全部是纯函数断言，不需要 Postgres（与 `TestBatchSelectStatementsMatchCanonicalColumns`
// 同一取向：默认路径 CI 可跑，不静默 skip）。

func TestSummarizePayloadReportsPerFieldLengths(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"question":  "冷链断链怎么处理？", // 9 字符
		"reasoning": "先识别约束，再推导。", // 10 字符
		"answer":    "结论：回滚批次。",   // 8 字符
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, chars, byField, _ := summarizePayload(payload)

	wantByField := map[string]int{
		"question":  9,
		"reasoning": 10,
		"answer":    8,
	}
	if !reflect.DeepEqual(byField, wantByField) {
		t.Fatalf("逐字段长度必须反映事实，want %v，实际 %v", wantByField, byField)
	}
	// 不变式：总量必须等于分列之和。否则「长度」与「字段清单」会互相矛盾，
	// 而用户正是拿这两个数去理解口径。
	total := 0
	for _, value := range byField {
		total += value
	}
	if chars != total {
		t.Fatalf("总字符数必须等于逐字段之和，chars=%d，sum=%d", chars, total)
	}
}

func TestSummarizePayloadExcludesNonContributingFields(t *testing.T) {
	// 边界：空串字段没有任何内容，不能出现在「长度由哪些字段构成」里 ——
	// 否则界面会声称「长度是 问题 + 推理过程 + 教师提示词 的合计」，
	// 而教师提示词实际是空的（那正是一种口径造假）。
	payload, err := json.Marshal(map[string]any{
		"question":      "题干",
		"reasoning":     "推导",
		"answer":        "答案",
		"teacherPrompt": "",
		"difficulty":    3, // 非字符串：不参与长度统计
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, _, byField, _ := summarizePayload(payload)
	if _, ok := byField["teacherPrompt"]; ok {
		t.Fatal("空串字段不得算作参与：界面会据此声称长度包含一个空字段")
	}
	if _, ok := byField["difficulty"]; ok {
		t.Fatal("非字符串字段不得参与长度统计")
	}
	if len(byField) != 3 {
		t.Fatalf("只应有 3 个字段参与，实际 %v", byField)
	}
}

func TestSummarizePayloadHandlesUnparsableInput(t *testing.T) {
	// 边界：payload 不是合法 JSON（历史脏数据）时不得报错、也不得编造读数。
	_, chars, byField, grounded := summarizePayload([]byte("{not json"))
	if chars != 0 || byField != nil || grounded {
		t.Fatalf("非法 JSON 必须给出零值事实，实际 chars=%d byField=%v grounded=%v", chars, byField, grounded)
	}

	_, chars, byField, grounded = summarizePayload(nil)
	if chars != 0 || byField != nil || grounded {
		t.Fatalf("空 payload 必须给出零值事实，实际 chars=%d byField=%v grounded=%v", chars, byField, grounded)
	}
}

func TestSummarizePayloadCountsTeacherPromptAndGroundedness(t *testing.T) {
	// 正常路径的另一半：`teacherPrompt` 是产出文本的一部分，必须计入；
	// 接地标记来自素材块字段，与长度互不干扰。
	payload, err := json.Marshal(map[string]any{
		"question":       "题干",
		"reasoning":      "推导",
		"answer":         "答案",
		"teacherPrompt":  "教师提示",
		"sourceChunkIds": []any{"chunk-1"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	_, _, byField, grounded := summarizePayload(payload)
	if _, ok := byField["teacherPrompt"]; !ok {
		t.Fatal("teacherPrompt 有内容时必须参与长度统计")
	}
	if len(byField) != 4 {
		t.Fatalf("应有 4 个字段参与，实际 %v", byField)
	}
	if !grounded {
		t.Fatal("sourceChunkIds 非空必须判为已接地")
	}
}
