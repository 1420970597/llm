package exporter

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// sampleRecords 构造覆盖常见边界的两条记录：
// 第一条含引号/逗号/换行（CSV 转义）、有打分；第二条无打分、思维链为空。
func sampleRecords() []Record {
	return []Record{
		{
			DatasetID:      1,
			DatasetName:    "军事领域",
			QuestionID:     11,
			Question:       "请分析\"联合作战\"中的指挥链，\n并给出三层要点",
			ChainOfThought: "第一步：界定指挥链层级。\n第二步：对照联合作战条令。",
			Answer:         "共三层：战略、战役、战术。",
			JudgePrompt:    "你是评委……",
			Difficulty:     "hard",
			DomainName:     "作战体系",
			RewardScore:    0.75,
			HasReward:      true,
			RewardLevels:   []string{"-1", "0", "1"},
		},
		{
			DatasetID:    1,
			DatasetName:  "军事领域",
			QuestionID:   12,
			Question:     "简述后勤保障",
			Answer:       "保障靠前配置。",
			Difficulty:   "easy",
			DomainName:   "装备发展",
			HasReward:    false,
			RewardLevels: []string{"-1", "0", "1"},
		},
	}
}

// TestFormatsMatchContract 断言注册表按冻结契约的顺序返回五种格式。
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.6 节。
func TestFormatsMatchContract(t *testing.T) {
	want := []string{"jsonl", "csv", "parquet", "alpaca", "sharegpt"}
	got := Formats()
	if len(got) != len(want) {
		t.Fatalf("格式数量不符: got %v want %v", got, want)
	}
	for index, name := range want {
		if got[index] != name {
			t.Fatalf("格式顺序不符: got %v want %v", got, want)
		}
	}
}

// TestJSONLOrderedAndTyped 断言 JSONL 保留配置的字段顺序，且数值字段不被转成字符串。
func TestJSONLOrderedAndTyped(t *testing.T) {
	exp, ok := Get("jsonl")
	if !ok {
		t.Fatal("jsonl 导出器未注册")
	}
	mapping := model.ExportMapping{FieldMap: map[string]any{
		"b_question": "{{question}}",
		"a_score":    "{{rewardScore}}",
	}}
	payload, err := exp.Encode(sampleRecords(), mapping)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望 2 行，实际 %d 行", len(lines))
	}
	// 映射按目标字段名字典序展开，a_score 应在 b_question 之前。
	if !strings.HasPrefix(lines[0], `{"a_score":0.75,"b_question":`) {
		t.Fatalf("字段顺序或类型不符: %s", lines[0])
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if score, isFloat := decoded["a_score"].(float64); !isFloat || score != 0.75 {
		t.Fatalf("rewardScore 应保留数值类型，实际 %#v", decoded["a_score"])
	}
	// 无打分记录时给空字符串，而不是 0——0 是合法分值。
	if decoded := mustDecode(t, lines[1]); decoded["a_score"] != "" {
		t.Fatalf("无打分记录应输出空字符串，实际 %#v", decoded["a_score"])
	}
}

func mustDecode(t *testing.T, line string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("输出不是合法 JSON: %v (%s)", err, line)
	}
	return out
}

// TestCSVEscapesQuotesAndNewlines 断言 CSV 正确转义引号、逗号与换行。
func TestCSVEscapesQuotesAndNewlines(t *testing.T) {
	exp, _ := Get("csv")
	payload, err := exp.Encode(sampleRecords(), model.ExportMapping{})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}

	rows, err := csv.NewReader(bytes.NewReader(payload)).ReadAll()
	if err != nil {
		t.Fatalf("输出不是合法 CSV: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("期望表头 + 2 行，实际 %d 行", len(rows))
	}
	if rows[0][0] != "question" {
		t.Fatalf("表头首列应为 question，实际 %q", rows[0][0])
	}
	// 原始问题里含双引号与换行，读回应与原文逐字一致。
	if rows[1][0] != sampleRecords()[0].Question {
		t.Fatalf("CSV 转义有损:\n got %q\nwant %q", rows[1][0], sampleRecords()[0].Question)
	}
}

// TestAlpacaShape 断言 Alpaca 三字段结构，且 input 为空。
func TestAlpacaShape(t *testing.T) {
	exp, _ := Get("alpaca")
	payload, err := exp.Encode(sampleRecords()[:1], model.ExportMapping{})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	decoded := mustDecode(t, strings.TrimSpace(string(payload)))

	if decoded["instruction"] != sampleRecords()[0].Question {
		t.Fatalf("instruction 应为问题，实际 %#v", decoded["instruction"])
	}
	if decoded["input"] != "" {
		t.Fatalf("input 应为空，实际 %#v", decoded["input"])
	}
	output, _ := decoded["output"].(string)
	if !strings.Contains(output, "答案：") {
		t.Fatalf("output 应包含思维链与答案，实际 %#v", decoded["output"])
	}
}

// TestShareGPTShape 断言 ShareGPT 的 human/gpt 两轮结构。
func TestShareGPTShape(t *testing.T) {
	exp, _ := Get("sharegpt")
	payload, err := exp.Encode(sampleRecords()[:1], model.ExportMapping{})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	var decoded struct {
		Conversations []struct {
			From  string `json:"from"`
			Value string `json:"value"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(payload), &decoded); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if len(decoded.Conversations) != 2 {
		t.Fatalf("期望 2 轮对话，实际 %d", len(decoded.Conversations))
	}
	if decoded.Conversations[0].From != "human" || decoded.Conversations[1].From != "gpt" {
		t.Fatalf("轮次角色不符: %#v", decoded.Conversations)
	}
	if decoded.Conversations[0].Value != sampleRecords()[0].Question {
		t.Fatalf("human 轮应为问题，实际 %#v", decoded.Conversations[0].Value)
	}
}

// TestParquetColumnsAlign 断言列式输出每列长度一致且列名完整。
func TestParquetColumnsAlign(t *testing.T) {
	exp, _ := Get("parquet")
	payload, err := exp.Encode(sampleRecords(), model.ExportMapping{})
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	if len(lines) == 0 {
		t.Fatal("列式输出为空")
	}
	for _, line := range lines {
		var column struct {
			Column string `json:"column"`
			Type   string `json:"type"`
			Values []any  `json:"values"`
		}
		if err := json.Unmarshal([]byte(line), &column); err != nil {
			t.Fatalf("列不是合法 JSON: %v", err)
		}
		if column.Column == "" {
			t.Fatalf("列名为空: %s", line)
		}
		if len(column.Values) != len(sampleRecords()) {
			t.Fatalf("列 %s 长度 %d，期望 %d", column.Column, len(column.Values), len(sampleRecords()))
		}
	}
}

// TestUnknownFieldIsRejected 断言映射引用未知字段时报错，而不是静默产出空列。
func TestUnknownFieldIsRejected(t *testing.T) {
	exp, _ := Get("jsonl")
	mapping := model.ExportMapping{FieldMap: map[string]any{"bad": "{{noSuchField}}"}}
	if _, err := exp.Encode(sampleRecords(), mapping); err == nil {
		t.Fatal("引用未知字段应当报错，实际通过了")
	} else if !strings.Contains(err.Error(), "noSuchField") {
		t.Fatalf("错误信息应包含字段名，实际: %v", err)
	}
}

// TestTemplateConcatenation 断言多占位符模板能拼接出思维链 + 答案。
func TestTemplateConcatenation(t *testing.T) {
	exp, _ := Get("alpaca")
	mapping := model.ExportMapping{FieldMap: map[string]any{
		"instruction": "{{question}}",
		"output":      "{{chainOfThought}}\n\n答案：{{answer}}",
	}}
	payload, err := exp.Encode(sampleRecords()[:1], mapping)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	decoded := mustDecode(t, strings.TrimSpace(string(payload)))
	output, _ := decoded["output"].(string)
	if !strings.Contains(output, "第一步：界定指挥链层级。") || !strings.Contains(output, "共三层") {
		t.Fatalf("模板拼接结果不完整: %#v", output)
	}
}

// TestBuiltinSpecsCoverFormats 断言内置映射的格式都真实注册过。
//
// 数量断言不是“凑数”：条目数变化时必须有人确认“新条目确实是内置契约”。
// T25 新增 `grpo-jsonl-v2`（结构化 GRPO 导出形状）因此从 3 变 4。
func TestBuiltinSpecsCoverFormats(t *testing.T) {
	specs := BuiltinSpecs()
	if len(specs) != 4 {
		t.Fatalf("期望 4 份内置映射，实际 %d", len(specs))
	}
	for _, spec := range specs {
		if _, ok := Get(spec.Format); !ok {
			t.Fatalf("内置映射 %s 引用了未注册的格式 %s", spec.Name, spec.Format)
		}
		if len(spec.FieldMap) == 0 {
			t.Fatalf("内置映射 %s 没有字段定义", spec.Name)
		}
	}

	// T25：结构化 GRPO 导出形状必须是**单占位符**，即 `{{field}}`。
	// 写成裸字段名或模板拼接会把数组压成字符串（`stringify` 对切片返回空串），
	// 而那种文件在字节层面看起来仍然“像 JSONL”。
	var structured *model.ExportMapping
	for index := range specs {
		if specs[index].Name == "grpo-jsonl-v2" {
			structured = &specs[index]
		}
	}
	if structured == nil {
		t.Fatal("必须存在 grpo-jsonl-v2（T25 的结构化 GRPO 映射）")
	}
	if structured.Format != model.ExportFormatJSONL {
		t.Fatalf("grpo-jsonl-v2 必须是 jsonl，实际 %s", structured.Format)
	}
	for _, field := range model.GRPORequiredExportFields() {
		source, found := structured.FieldMap[field]
		if !found {
			t.Fatalf("grpo-jsonl-v2 缺少必需字段 %q", field)
		}
		expr, _ := source.(string)
		trimmed := strings.TrimSpace(expr)
		if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") ||
			strings.Count(trimmed, "{{") != 1 {
			t.Fatalf("字段 %q 必须是单个占位符（如 {{{{levels}}}}），实际 %q", field, expr)
		}
	}
	// 旧映射保留 `reward_levels`（逗号串）以免破坏第一轮导出。
	legacy := false
	for _, spec := range specs {
		if spec.Name == "grpo-jsonl" {
			legacy = true
			if _, found := spec.FieldMap["reward_levels"]; !found {
				t.Fatal("旧 grpo-jsonl 必须保留 reward_levels（第一轮导出仍在使用）")
			}
		}
	}
	if !legacy {
		t.Fatal("必须保留旧 grpo-jsonl：新形状与旧导出隔离共存，不就地修改旧映射")
	}
}

// TestNormalizeKeyVariants 断言 camelCase / 点号 / 短横线写法都能命中同一字段。
func TestNormalizeKeyVariants(t *testing.T) {
	cases := map[string]string{
		"chainOfThought":   "chain_of_thought",
		"chain.of.thought": "chain_of_thought",
		"chain-of-thought": "chain_of_thought",
		"CHAIN_OF_THOUGHT": "chain_of_thought",
		" rewardScore ":    "reward_score",
	}
	for input, want := range cases {
		if got := normalizeKey(input); got != want {
			t.Fatalf("normalizeKey(%q) = %q, want %q", input, got, want)
		}
	}
}
