package main

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
)

// 本文件验证 T23 的 GRPO payload 组装（纯函数）。
//
// 这里最危险的错误是把 SFT 的字段带进 GRPO payload（或反过来）：
// 两种训练数据混在一起后，下游无法区分，而质量实验会按错误的量表打分。

func grpoOutputFixture() llm.GrpoPromptOutput {
	return llm.GrpoPromptOutput{
		JudgePrompt: "请判断回答是否……",
		LevelRubrics: []model.GrpoLevelRubric{
			{Level: "-1", Criteria: "答非所问", AcceptCase: "完全不相关", RejectCase: "给出了正确方向"},
			{Level: "0", Criteria: "部分正确", AcceptCase: "方向对但缺步骤", RejectCase: "完全跑题"},
			{Level: "1", Criteria: "完整且可核对", AcceptCase: "每步可核对", RejectCase: "结论无依据"},
		},
		FrameworkRef: "chain-standard:v3",
	}
}

// TestBuildGrpoPayloadShape 覆盖 GRPO payload 的字段结构（§2.2）。
func TestBuildGrpoPayloadShape(t *testing.T) {
	levels := []string{"-1", "0", "1"}
	payload, err := buildGrpoPayload("冷链温控怎么做", grpoOutputFixture(), levels)
	if err != nil {
		t.Fatalf("buildGrpoPayload: %v", err)
	}

	// 四个必需字段 + 框架来源。
	for _, key := range []string{"question", "judgePrompt", "levels", "levelRubrics", "frameworkRef"} {
		if _, found := payload[key]; !found {
			t.Fatalf("payload 缺少字段 %q，实际 %+v", key, payload)
		}
	}
	// **levels 必须是数组**（不得 join 成逗号字符串）。
	gotLevels, ok := payload["levels"].([]string)
	if !ok {
		t.Fatalf("levels 必须是字符串数组，实际 %T", payload["levels"])
	}
	if len(gotLevels) != 3 || gotLevels[0] != "-1" || gotLevels[2] != "1" {
		t.Fatalf("档位必须保留原顺序（-1 → 0 → 1 由低到高），实际 %v", gotLevels)
	}
	// 判据与档位一一对应，且每条至少有一个边界例。
	rubrics, ok := payload["levelRubrics"].([]map[string]any)
	if !ok {
		t.Fatalf("levelRubrics 必须是对象数组，实际 %T", payload["levelRubrics"])
	}
	if len(rubrics) != 3 {
		t.Fatalf("判据数必须与档位数一致，实际 %d vs %d", len(rubrics), len(gotLevels))
	}
	for index, rubric := range rubrics {
		if rubric["level"] != gotLevels[index] {
			t.Fatalf("第 %d 条判据的档位必须是 %q，实际 %v", index, gotLevels[index], rubric["level"])
		}
		if rubric["acceptCase"] == "" && rubric["rejectCase"] == "" {
			t.Fatalf("第 %d 条判据必须至少给一个方向上的边界例", index)
		}
	}
}

// TestBuildGrpoPayloadHasNoSftFields 覆盖 T23 的核心禁令：
// **不得转写 reward_records 或伪造 SFT answer**。
func TestBuildGrpoPayloadHasNoSftFields(t *testing.T) {
	// levels 必须与判据的档位一致：T05 的 schema 会拒绝「判据档位不在 levels 中」，
	// 而初版夹具正是那样（3 条判据配 2 档 levels）——校验当场拒绝，这是对的。
	payload, err := buildGrpoPayload("问题", grpoOutputFixture(), []string{"-1", "0", "1"})
	if err != nil {
		t.Fatalf("buildGrpoPayload: %v", err)
	}
	// SFT 的字段绝不能出现在 GRPO payload 里。
	for _, forbidden := range []string{"answer", "reasoning", "chainOfThought", "chain_of_thought", "rewardScore"} {
		if _, found := payload[forbidden]; found {
			t.Fatalf("GRPO payload 不得包含 SFT 字段 %q（两种训练数据混在一起后下游无法区分）", forbidden)
		}
	}
	// 也不能出现「判据被 join 成字符串」的形态。
	if text, ok := payload["levels"].(string); ok && strings.Contains(text, ",") {
		t.Fatalf("levels 不得被 join 成逗号字符串，实际 %q", text)
	}
}

// TestBuildGrpoPayloadRejectsInvalidLevelsAndRubrics 覆盖验收项
// 「schema 校验覆盖缺档、重复档、空规则、坏 JSON」。
//
// 生成的判据不合规必须在**写入之前**失败：静默写入会让质量实验按错误的
// 量表打分，而那时已经花了两次模型调用。
func TestBuildGrpoPayloadRejectsInvalidLevelsAndRubrics(t *testing.T) {
	cases := []struct {
		name     string
		levels   []string
		output   llm.GrpoPromptOutput
		question string
	}{
		{
			name: "档位不足两档", levels: []string{"1"},
			output: grpoOutputFixture(), question: "问题",
		},
		{
			name: "档位重复", levels: []string{"1", "1"},
			output: grpoOutputFixture(), question: "问题",
		},
		{
			name: "缺少判据", levels: []string{"-1", "0", "1"},
			output: llm.GrpoPromptOutput{JudgePrompt: "提示词"}, question: "问题",
		},
		{
			name: "判据缺档位", levels: []string{"-1", "1"},
			output: llm.GrpoPromptOutput{JudgePrompt: "提示词",
				LevelRubrics: []model.GrpoLevelRubric{{Criteria: "有判据"}}},
			question: "问题",
		},
		{
			name: "判据为空规则", levels: []string{"-1", "1"},
			output: llm.GrpoPromptOutput{JudgePrompt: "提示词",
				LevelRubrics: []model.GrpoLevelRubric{
					{Level: "-1"}, {Level: "1", Criteria: "有判据"},
				}},
			question: "问题",
		},
		{
			name: "没有问题内容", levels: []string{"-1", "1"},
			output: grpoOutputFixture(), question: "   ",
		},
		{
			name: "没有裁判提示词", levels: []string{"-1", "1"},
			output:   llm.GrpoPromptOutput{LevelRubrics: grpoOutputFixture().LevelRubrics},
			question: "问题",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload, err := buildGrpoPayload(testCase.question, testCase.output, testCase.levels)
			if err == nil {
				t.Fatalf("不合规的产物必须被拒绝，实际 payload=%+v", payload)
			}
			// 错误必须可读（面向运维，而不是「invalid payload」）。
			if strings.TrimSpace(err.Error()) == "" {
				t.Fatal("必须给出可读原因")
			}
		})
	}
}

// TestBuildGrpoPayloadNormalizesQuestionWhitespace 覆盖「问题去空白后判空」。
func TestBuildGrpoPayloadKeepsQuestionAsGiven(t *testing.T) {
	payload, err := buildGrpoPayload("冷链温控怎么做", grpoOutputFixture(), []string{"-1", "0", "1"})
	if err != nil {
		t.Fatalf("buildGrpoPayload: %v", err)
	}
	if payload["question"] != "冷链温控怎么做" {
		t.Fatalf("问题内容必须原样保留（不加工），实际 %v", payload["question"])
	}
}

// TestGrpoAndSftShareLifecycleButDifferInSchema 断言两条分支的 schema 不同
// 且都被 T05 的 schema 版本区分。
func TestGrpoAndSftShareLifecycleButDifferInSchema(t *testing.T) {
	grpoSchema := model.SampleSchemaForTarget(model.TargetKindGRPO)
	sftSchema := model.SampleSchemaForTarget(model.TargetKindSFT)
	if grpoSchema == sftSchema {
		t.Fatal("SFT 与 GRPO 必须使用不同的 sample schema 标识（否则下游无法区分）")
	}
	if !strings.HasPrefix(grpoSchema, "grpo.") || !strings.HasPrefix(sftSchema, "sft.") {
		t.Fatalf("schema 标识必须能看出目标类型，实际 %q / %q", grpoSchema, sftSchema)
	}
}
