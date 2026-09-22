package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// 本文件验证 Issue #160 T25 的 GRPO 导出结构契约。
//
// 全部是纯函数断言：T25 的三条核心要求（保留数组/对象结构、档位一一对应、
// 坏结构必须被拒绝）都不依赖数据库或模型。

func grpoExportLineForTest() GRPOExportLine {
	return GRPOExportLine{
		Question:    "解释快速排序的分区",
		JudgePrompt: "按档位评分：基础、精通。",
		Levels:      []string{"基础", "精通"},
		LevelRubrics: []GRPORubricExport{
			{Level: "基础", Criteria: "能说出思路", AcceptCase: "提到 pivot", RejectCase: "答非所问"},
			{Level: "精通", Criteria: "能讨论退化", AcceptCase: "指出有序输入", RejectCase: "否认退化"},
		},
		FrameworkRef: "teacher-v1",
	}
}

func marshalExportLine(t *testing.T, line GRPOExportLine) []byte {
	t.Helper()
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// TestVerifyGRPOExportLineAcceptsStructuredLine 覆盖正常路径，
// 并断言导出的**字段名与结构**符合 T25 的逐行解码要求。
func TestVerifyGRPOExportLineAcceptsStructuredLine(t *testing.T) {
	raw := marshalExportLine(t, grpoExportLineForTest())
	for _, required := range GRPORequiredExportFields() {
		if !strings.Contains(string(raw), `"`+required+`"`) {
			t.Fatalf("导出行必须包含字段 %q，实际 %s", required, raw)
		}
	}
	// levels 必须是数组、level_rubrics 必须是对象数组。
	var probe struct {
		Levels       []string           `json:"levels"`
		LevelRubrics []GRPORubricExport `json:"level_rubrics"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("levels/level_rubrics 必须能解码成数组结构：%v（实际 %s）", err, raw)
	}
	if len(probe.Levels) != 2 || len(probe.LevelRubrics) != 2 {
		t.Fatalf("结构与档位必须一一对应，实际 %d 档 / %d 条判据", len(probe.Levels), len(probe.LevelRubrics))
	}
	if err := VerifyGRPOExportLine(raw); err != nil {
		t.Fatalf("合法行不应被拒绝：%v", err)
	}
}

// TestVerifyGRPOExportLineRejectsCommaJoinedLevels 覆盖 T25 明令禁止的退化形态：
// 把 levels 压成逗号字符串（第一轮 `reward_levels` 的行为）。
func TestVerifyGRPOExportLineRejectsCommaJoinedLevels(t *testing.T) {
	raw := []byte(`{"question":"q","judge_prompt":"p","levels":"基础,精通","level_rubrics":[{"level":"基础","criteria":"c"},{"level":"精通","criteria":"c"}]}`)
	if err := VerifyGRPOExportLine(raw); err == nil {
		t.Fatal("逗号字符串形式的 levels 必须被拒绝（结构被压平）")
	}
}

// TestVerifyGRPOExportLineRejectsRubricLevelMismatch 覆盖「档位与判据一一对应」。
func TestVerifyGRPOExportLineRejectsRubricLevelMismatch(t *testing.T) {
	cases := []struct {
		name string
		line GRPOExportLine
	}{
		{"判据少一条", GRPOExportLine{
			Question: "q", JudgePrompt: "p",
			Levels: []string{"a", "b"},
			LevelRubrics: []GRPORubricExport{
				{Level: "a", Criteria: "c"},
			},
		}},
		{"判据档位不在 levels 里", GRPOExportLine{
			Question: "q", JudgePrompt: "p",
			Levels: []string{"a", "b"},
			LevelRubrics: []GRPORubricExport{
				{Level: "a", Criteria: "c"}, {Level: "c", Criteria: "c"},
			},
		}},
		{"判据缺文本", GRPOExportLine{
			Question: "q", JudgePrompt: "p",
			Levels: []string{"a", "b"},
			LevelRubrics: []GRPORubricExport{
				{Level: "a", Criteria: "c"}, {Level: "b"},
			},
		}},
		{"档位重复", GRPOExportLine{
			Question: "q", JudgePrompt: "p",
			Levels: []string{"a", "a"},
			LevelRubrics: []GRPORubricExport{
				{Level: "a", Criteria: "c"}, {Level: "a", Criteria: "c"},
			},
		}},
		{"档位不足两档", GRPOExportLine{
			Question: "q", JudgePrompt: "p",
			Levels: []string{"a"},
			LevelRubrics: []GRPORubricExport{
				{Level: "a", Criteria: "c"},
			},
		}},
		{"缺少 judge_prompt", GRPOExportLine{
			Question: "q",
			Levels:   []string{"a", "b"},
			LevelRubrics: []GRPORubricExport{
				{Level: "a", Criteria: "c"}, {Level: "b", Criteria: "c"},
			},
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := VerifyGRPOExportLine(marshalExportLine(t, testCase.line)); err == nil {
				t.Fatalf("必须被拒绝：%+v", testCase.line)
			}
		})
	}
}

// TestVerifyGRPOExportLinesCountsAndReportsLine 覆盖「行数与行号」：
// T25 要求清单与实际行数一致，而「哪一行坏了」是用户唯一能行动的线索。
func TestVerifyGRPOExportLinesCountsAndReportsLine(t *testing.T) {
	first := marshalExportLine(t, grpoExportLineForTest())
	second := marshalExportLine(t, grpoExportLineForTest())
	// 尾随换行是正常的 JSONL 结尾，不应被算成空行。
	content := append(append(append([]byte{}, first...), '\n'), second...)
	content = append(content, '\n')

	count, err := VerifyGRPOExportLines(content)
	if err != nil {
		t.Fatalf("合法文件不应报错：%v", err)
	}
	if count != 2 {
		t.Fatalf("有效行数应为 2，实际 %d", count)
	}

	// 中间插入坏行：错误必须带行号。
	broken := append([]byte{}, first...)
	broken = append(broken, '\n')
	broken = append(broken, []byte(`{"question":"q"}`)...)
	broken = append(broken, '\n')
	broken = append(broken, second...)
	broken = append(broken, '\n')
	if _, err := VerifyGRPOExportLines(broken); err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Fatalf("坏行错误必须带行号，实际 %v", err)
	}

	if _, err := VerifyGRPOExportLines([]byte("\n\n")); err == nil {
		t.Fatal("全空内容必须被拒绝")
	}
}

// TestToGRPORubricExportsPreservesFields 覆盖判据字段的完整搬运：
// 少搬一个字段（例如 accept_case）会让档位边界在文件里消失，
// 而文件本身看起来仍然完全正常。
func TestToGRPORubricExportsPreservesFields(t *testing.T) {
	exports := ToGRPORubricExports([]GrpoLevelRubric{{
		Level: "a", Criteria: "c", AcceptCase: "acc", RejectCase: "rej",
	}})
	if len(exports) != 1 {
		t.Fatalf("应得到 1 条判据，实际 %d", len(exports))
	}
	want := GRPORubricExport{Level: "a", Criteria: "c", AcceptCase: "acc", RejectCase: "rej"}
	if exports[0] != want {
		t.Fatalf("判据字段必须完整搬运：%+v vs %+v", exports[0], want)
	}
	raw, err := json.Marshal(exports[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"level", "criteria", "accept_case", "reject_case"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("导出判据必须用 snake_case 键 %q，实际 %s", key, raw)
		}
	}
}
