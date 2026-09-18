package llm

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

func TestNormalizeLevelsDropsBlanksAndDuplicatesKeepingOrder(t *testing.T) {
	got := NormalizeLevels([]string{" -1 ", "", "0", "-1", "  1  "})
	want := []string{"-1", "0", "1"}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order mismatch at %d: got %v want %v", index, got, want)
		}
	}
}

func TestNormalizeLevelsPreservesDescendingOrder(t *testing.T) {
	// 档次顺序有语义（高→低），不得被排序打乱。
	got := NormalizeLevels([]string{"1", "0", "-1"})
	want := []string{"1", "0", "-1"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("order mismatch: got %v want %v", got, want)
		}
	}
}

func TestMapRubricsToLevelsRejectsMissingLevel(t *testing.T) {
	generated := grpoRubricPayload{
		LevelRubrics: []struct {
			Level      string `json:"level"`
			Label      string `json:"label"`
			Criteria   string `json:"criteria"`
			AcceptCase string `json:"acceptCase"`
			RejectCase string `json:"rejectCase"`
		}{
			{Level: "-1", Label: "差", Criteria: "未覆盖思考框架"},
			{Level: "0", Label: "中", Criteria: "部分覆盖"},
		},
	}

	if _, err := mapRubricsToLevels(generated, []string{"-1", "0", "1"}); err == nil {
		t.Fatal("expected error when a reward level has no criteria, got nil")
	}
}

func TestMapRubricsToLevelsRejectsEmptyCriteria(t *testing.T) {
	generated := grpoRubricPayload{
		LevelRubrics: []struct {
			Level      string `json:"level"`
			Label      string `json:"label"`
			Criteria   string `json:"criteria"`
			AcceptCase string `json:"acceptCase"`
			RejectCase string `json:"rejectCase"`
		}{
			{Level: "-1", Label: "差", Criteria: "有判据"},
			{Level: "0", Label: "中", Criteria: "   "},
			{Level: "1", Label: "好", Criteria: "有判据"},
		},
	}

	_, err := mapRubricsToLevels(generated, []string{"-1", "0", "1"})
	if err == nil {
		t.Fatal("expected error when a level has blank criteria, got nil")
	}
	if !strings.Contains(err.Error(), "0") {
		t.Fatalf("error should name the offending level, got %q", err.Error())
	}
}

func TestMapRubricsToLevelsFillsDefaultLabel(t *testing.T) {
	generated := grpoRubricPayload{
		LevelRubrics: []struct {
			Level      string `json:"level"`
			Label      string `json:"label"`
			Criteria   string `json:"criteria"`
			AcceptCase string `json:"acceptCase"`
			RejectCase string `json:"rejectCase"`
		}{
			{Level: "-1", Criteria: "判据一"},
			{Level: "1", Criteria: "判据二"},
		},
	}

	rubrics, err := mapRubricsToLevels(generated, []string{"-1", "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rubrics) != 2 {
		t.Fatalf("expected 2 rubrics, got %d", len(rubrics))
	}
	if rubrics[0].Label != "档次 -1" {
		t.Fatalf("expected default label, got %q", rubrics[0].Label)
	}
	if rubrics[0].Level != "-1" || rubrics[1].Level != "1" {
		t.Fatalf("levels must follow user order, got %q then %q", rubrics[0].Level, rubrics[1].Level)
	}
}

func TestBuildJudgePromptContainsAllMandatorySections(t *testing.T) {
	input := GrpoPromptInput{
		RootKeyword:   "军事",
		DirectionName: "海上巡逻",
		Question:      "在A海域有巡逻编队，遇到不明船只，请做出规划。",
		ChainSteps: []model.ChainStep{
			{Index: 1, Title: "态势研判", Description: "确认目标性质与意图", Checkpoint: "是否完成敌我识别"},
			{Index: 2, Title: "方案生成", Description: "给出可选处置方案"},
		},
		Levels: []string{"-1", "0", "1"},
	}
	rubrics := []model.GrpoLevelRubric{
		{Level: "-1", Label: "不合格", Criteria: "未覆盖态势研判", AcceptCase: "直接交火", RejectCase: "完整研判"},
		{Level: "0", Label: "基本合格", Criteria: "部分覆盖", AcceptCase: "仅研判未给方案", RejectCase: "完整覆盖"},
		{Level: "1", Label: "优秀", Criteria: "全步骤覆盖", AcceptCase: "逐步研判并给方案", RejectCase: "缺步骤"},
	}

	prompt := buildJudgePrompt(input, input.Levels, rubrics)

	mustContain := []string{
		"## 一、评审对象",            // 1 角色与对象
		"## 二、整体性思考框架（必须逐步核对）", // 2 框架
		"## 三、打分档次与判据",         // 3 判据
		"## 四、结合具体场景的判断要求",     // 4 场景判断
		"## 五、输出格式（强制）",        // 5 输出格式
		"你是资深的长链思考数据评审专家",      // 角色设定
		"海上巡逻",      // 方向
		"在A海域有巡逻编队", // 问题场景
		"态势研判",      // 标准步骤标题
		"是否完成敌我识别",  // 检查点
		"档次 `-1`",   // 每档判据
		"档次 `0`",
		"档次 `1`",
		`{"level":"<-1|0|1>"`, // 强制 JSON 格式且列出全部档次
	}
	for _, needle := range mustContain {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("judge prompt missing required content %q", needle)
		}
	}
}

func TestBuildJudgePromptDeclaresMissingFrameworkWhenNoChainSteps(t *testing.T) {
	input := GrpoPromptInput{
		RootKeyword:   "军事",
		DirectionName: "海上巡逻",
		Question:      "问题",
		Levels:        []string{"-1", "1"},
	}
	rubrics := []model.GrpoLevelRubric{
		{Level: "-1", Criteria: "判据"},
		{Level: "1", Criteria: "判据"},
	}

	prompt := buildJudgePrompt(input, input.Levels, rubrics)

	if !strings.Contains(prompt, "本方向尚未提供长链思维标准步骤") {
		t.Fatal("prompt must explicitly declare the missing thinking framework")
	}
	if !strings.Contains(prompt, "从严评判其推理完整性") {
		t.Fatal("prompt must instruct stricter judging when framework is absent")
	}
}

func TestFrameworkReferenceCountsSteps(t *testing.T) {
	withSteps := frameworkReference(GrpoPromptInput{
		DirectionName: "海上巡逻",
		ChainSteps:    []model.ChainStep{{Index: 1}, {Index: 2}, {Index: 3}},
	})
	if withSteps != "海上巡逻 · 3 步标准步骤" {
		t.Fatalf("unexpected framework ref: %q", withSteps)
	}

	withoutSteps := frameworkReference(GrpoPromptInput{DirectionName: "海上巡逻"})
	if withoutSteps != "海上巡逻 · 无标准步骤" {
		t.Fatalf("unexpected framework ref: %q", withoutSteps)
	}
}

func TestGenerateGrpoPromptRejectsInsufficientLevels(t *testing.T) {
	// 单档次无法构成打分区间，必须在调用模型前就拒绝。
	_, err := GenerateGrpoPrompt(nil, ProviderConfig{BaseURL: "http://x", APIKey: "k"}, GrpoPromptInput{
		Question: "问题",
		Levels:   []string{"1"},
	})
	if err == nil {
		t.Fatal("expected error for single reward level, got nil")
	}
	if !strings.Contains(err.Error(), "at least two reward levels") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGenerateGrpoPromptRejectsMockProvider(t *testing.T) {
	_, err := GenerateGrpoPrompt(nil, ProviderConfig{ProviderType: "mock"}, GrpoPromptInput{
		Question: "问题",
		Levels:   []string{"-1", "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "mock provider is disabled") {
		t.Fatalf("mock provider must be rejected, got %v", err)
	}
}

func TestGenerateGrpoPromptRejectsIncompleteProvider(t *testing.T) {
	_, err := GenerateGrpoPrompt(nil, ProviderConfig{BaseURL: "", APIKey: ""}, GrpoPromptInput{
		Question: "问题",
		Levels:   []string{"-1", "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "configuration is incomplete") {
		t.Fatalf("incomplete provider must be rejected, got %v", err)
	}
}

func TestGenerateGrpoPromptRejectsEmptyQuestion(t *testing.T) {
	_, err := GenerateGrpoPrompt(nil, ProviderConfig{BaseURL: "http://x", APIKey: "k"}, GrpoPromptInput{
		Question: "   ",
		Levels:   []string{"-1", "1"},
	})
	if err == nil || !strings.Contains(err.Error(), "question content is required") {
		t.Fatalf("empty question must be rejected, got %v", err)
	}
}
