package studio

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件验证数据集分析里的**长度口径**（issue #197 第 13 条）。
//
// 缺陷形态：`LengthAnalysis.FieldCount` 硬编码为 1，而读数实际是多个文本字段的
// 合计 —— API 自称「参与统计的字段数 1」，界面上也没有任何口径说明。
// 用户看到「长度中位 1082」会把它读成单条内容的长度（例如只看问题），
// 而它是问题+推理+答案之和。数字可见但口径不可见。
//
// 断言覆盖正常路径（多字段命中）与边界路径（字段集不完整、空集合），
// 且都是纯函数断言：不需要 Postgres，CI 默认路径可跑（不静默 skip）。

func TestSummarizeLengthsReportsActualFieldSet(t *testing.T) {
	fields := model.SampleLengthFields() // question / reasoning / answer / teacherPrompt

	length := summarizeLengths([]int{10, 20, 30, 40}, fields)
	if length == nil {
		t.Fatal("有数据时必须给出长度分布")
	}
	// 「字段数」必须是**事实**，不是常数。
	if length.FieldCount != len(fields) {
		t.Fatalf("FieldCount 必须等于实际参与字段数，want %d，实际 %d", len(fields), length.FieldCount)
	}
	if len(length.Fields) != len(fields) {
		t.Fatalf("字段清单必须带出，want %v，实际 %v", fields, length.Fields)
	}
	// 分位仍是最近秩法（返回真实存在的长度，不插值）。
	if length.P50 != 20 || length.P90 != 40 {
		t.Fatalf("最近秩法读数为 P50=20 / P90=40，实际 P50=%d / P90=%d", length.P50, length.P90)
	}
	if length.MeanChars != 25 {
		t.Fatalf("均值应为 25，实际 %d", length.MeanChars)
	}
}

func TestSummarizeLengthsHandlesSingleField(t *testing.T) {
	// 边界：只命中 1 个字段（例如历史数据只有 question）时，FieldCount 必须是 1
	// 且字段清单只有那一项 —— 这正是旧实现**碰巧**说出的事实，
	// 但它同时也证明了旧实现把「单字段」当成了永远成立的前提。
	length := summarizeLengths([]int{7}, []string{"question"})
	if length.FieldCount != 1 || len(length.Fields) != 1 || length.Fields[0] != "question" {
		t.Fatalf("单字段读数错误：%+v", length)
	}
}

func TestLengthFieldsOfUnionsAcrossSamples(t *testing.T) {
	// 正常路径：批次里不同样本写了不同字段集时必须取**并集**。
	// 只取首条会把「有的样本算了 4 个字段」抹掉，而用户面对的是整批的读数。
	rows := []store.SampleVersionFact{
		{LengthByField: map[string]int{"question": 5, "reasoning": 9, "answer": 3}},
		{LengthByField: map[string]int{"question": 4, "reasoning": 8, "answer": 2, "teacherPrompt": 6}},
	}

	fields := lengthFieldsOf(rows)
	want := model.SampleLengthFields()
	if strings.Join(fields, ",") != strings.Join(want, ",") {
		t.Fatalf("字段并集必须按 model.SampleLengthFields 的全序输出，want %v，实际 %v", want, fields)
	}
}

func TestLengthFieldsOfIsEmptyForSamplesWithoutLengthFields(t *testing.T) {
	// 边界：样本 payload 里一个长度字段都没有时，不得凭空声称有字段参与。
	fields := lengthFieldsOf([]store.SampleVersionFact{{LengthByField: nil}})
	if len(fields) != 0 {
		t.Fatalf("无字段命中时必须为空集合，实际 %v", fields)
	}
	if fields == nil {
		t.Fatal("必须返回空切片而不是 nil：JSON 序列化后应为 [] 而不是 null")
	}
}

func TestSampleLengthFieldsIsStableOrder(t *testing.T) {
	// 口径必须是可重放的：同一份 payload 每次得到同一个读数。
	first := model.SampleLengthFields()
	second := model.SampleLengthFields()
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Fatalf("字段顺序必须稳定，%v vs %v", first, second)
	}
	// 必填字段必须都在长度口径里，否则「长度」会漏掉用户认为必然存在的部分。
	for _, required := range model.RequiredSampleFields(model.TargetKindSFT) {
		found := false
		for _, key := range first {
			if key == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("SFT 必填字段 %q 必须参与长度统计，实际口径 %v", required, first)
		}
	}
}
