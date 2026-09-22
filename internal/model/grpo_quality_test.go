package model

import (
	"encoding/json"
	"testing"
)

// 本文件验证 Issue #160 T24 的 GRPO 质量判据（确定性部分）。
//
// 全部不依赖模型与数据库：档位覆盖是纯函数，边界参考集 hash 只做规范化，
// 目标配置只做形状校验。模型裁判的输入构造在 internal/eval 里另测。

func completeGRPOSample() GRPOSample {
	return GRPOSample{
		Question:    "为什么快速排序最坏是 O(n^2)",
		JudgePrompt: "按档位评分：基础、熟练、精通。",
		Levels:      []string{"基础", "熟练", "精通"},
		LevelRubrics: []GrpoLevelRubric{
			{Level: "基础", Criteria: "能说出基准", AcceptCase: "提到 pivot", RejectCase: "答非所问"},
			{Level: "熟练", Criteria: "能算复杂度", AcceptCase: "写 O(n log n)", RejectCase: "复杂度写错"},
			{Level: "精通", Criteria: "能讨论退化", AcceptCase: "指出有序输入", RejectCase: "否认退化"},
		},
	}
}

// TestBuildLevelCoverageAllCovered 覆盖正常路径：每一档都有判据与边界例，
// 且判据在裁判提示词里被提到 → 覆盖 1.0。
func TestBuildLevelCoverageAllCovered(t *testing.T) {
	coverage := BuildLevelCoverage(completeGRPOSample())
	if !coverage.Evaluable {
		t.Fatal("有档位时应当可判定")
	}
	if coverage.Score != 1 {
		t.Fatalf("全部覆盖应得 1.0，实际 %v（原因：%v）", coverage.Score, coverage.Reasons)
	}
	if len(coverage.MissingLevels) != 0 {
		t.Fatalf("不应有缺失档位，实际 %v", coverage.MissingLevels)
	}
}

// TestBuildLevelCoverageFraction 覆盖「部分覆盖」：一档缺判据文本。
func TestBuildLevelCoverageFraction(t *testing.T) {
	sample := completeGRPOSample()
	sample.LevelRubrics[1].Criteria = ""
	coverage := BuildLevelCoverage(sample)
	if coverage.Score != 2.0/3.0 {
		t.Fatalf("2/3 档有依据应得 %.4f，实际 %v", 2.0/3.0, coverage.Score)
	}
	if len(coverage.MissingLevels) != 1 || coverage.MissingLevels[0] != "熟练" {
		t.Fatalf("缺失档位应为 [熟练]，实际 %v", coverage.MissingLevels)
	}
}

// TestBuildLevelCoverageRequiresJudgePromptMention 覆盖「判据写了但提示词没提」
// 这条文本级检查：模型不会去区分它没被要求区分的档位。
func TestBuildLevelCoverageRequiresJudgePromptMention(t *testing.T) {
	sample := completeGRPOSample()
	sample.JudgePrompt = "按档位评分：基础、熟练。" // 少了「精通」
	coverage := BuildLevelCoverage(sample)
	if coverage.Score != 2.0/3.0 {
		t.Fatalf("提示词未提及的档位不算覆盖，实际覆盖 %v", coverage.Score)
	}
	found := false
	for _, reason := range coverage.Reasons {
		if reason == "档位 \"精通\" 未出现在裁判提示词里（判据写了但提示词没提，模型不会去区分它）" {
			found = true
		}
	}
	if !found {
		t.Fatalf("必须给出可操作原因，实际 %v", coverage.Reasons)
	}
}

// TestBuildLevelCoverageNotEvaluableWithoutLevels 覆盖
// 「档位覆盖无法判定」→ 调用方必须记缺分而不是 0 分。
func TestBuildLevelCoverageNotEvaluableWithoutLevels(t *testing.T) {
	coverage := BuildLevelCoverage(GRPOSample{})
	if coverage.Evaluable {
		t.Fatal("没有档位时应标记为不可判定")
	}
	if len(coverage.Reasons) == 0 {
		t.Fatal("不可判定必须带原因")
	}
}

// TestValidationRejectsLevelWithoutBoundaryCase 覆盖 §2.2
// 「每档至少包含判据文本与边界例」。
func TestValidationRejectsLevelWithoutBoundaryCase(t *testing.T) {
	sample := completeGRPOSample()
	sample.LevelRubrics[0].AcceptCase = ""
	sample.LevelRubrics[0].RejectCase = ""
	if err := ValidateGRPOSamplePayload(sample.Levels, sample.LevelRubrics); err == nil {
		t.Fatal("没有任何边界例的档位必须被判据校验拒绝")
	}
}

// TestBoundaryReferenceHashIsOrderInsensitive 覆盖「同一份参考集得到同一 hash」。
//
// 客户端提交顺序不同（例如界面按选择顺序拼数组）不应该让冻结标识变化。
func TestBoundaryReferenceHashIsOrderInsensitive(t *testing.T) {
	first := BoundaryReferenceSet{ID: "br", Source: "s", Items: []BoundaryReferenceItem{
		{Level: "a", Input: "x", Expected: BoundaryExpectedAccept},
		{Level: "b", Input: "y", Expected: BoundaryExpectedReject},
	}}
	second := BoundaryReferenceSet{ID: "br", Source: "s", Items: []BoundaryReferenceItem{
		{Level: "b", Input: "y", Expected: BoundaryExpectedReject},
		{Level: "a", Input: "x", Expected: BoundaryExpectedAccept},
	}}
	firstHash, err := first.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	secondHash, err := second.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("顺序不同的同一份参考集必须得到同一 hash：%s vs %s", firstHash, secondHash)
	}
	changed := second
	changed.Items = append([]BoundaryReferenceItem{}, second.Items...)
	changed.Items[0].Expected = BoundaryExpectedAccept
	changedHash, err := changed.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if changedHash == firstHash {
		t.Fatal("内容不同必须得到不同 hash")
	}
}

// TestGRPOTargetConfigPrepareAndParse 覆盖目标配置的规范化与解析。
func TestGRPOTargetConfigPrepareAndParse(t *testing.T) {
	config := GRPOTargetConfig{
		TeacherPromptVersion:  "teacher-v3",
		BaselineAnswerVersion: "baseline-2",
		BoundaryReference: BoundaryReferenceSet{
			ID: "br", Source: "人工", Sampled: true,
			Items: []BoundaryReferenceItem{
				{Level: "基础", Input: "x", Expected: BoundaryExpectedAccept},
			},
		},
	}
	if err := config.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if config.BoundaryReferenceHash == "" {
		t.Fatal("Prepare 必须写入参考集 hash")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseGRPOTargetConfig(encoded)
	if err != nil {
		t.Fatalf("ParseGRPOTargetConfig: %v", err)
	}
	if parsed.BoundaryReferenceHash != config.BoundaryReferenceHash {
		t.Fatalf("round-trip 应保留 hash：%s vs %s", parsed.BoundaryReferenceHash, config.BoundaryReferenceHash)
	}
	if !parsed.HasBoundaryReference() {
		t.Fatal("有样例时应判定为存在参考集")
	}
}

// TestGRPOTargetConfigValidateRejectsBadExpected 覆盖参考集的期望值必须是
// 闭合集合（accept/reject）：自由文本会让「和冻结标注比对」无从实现。
func TestGRPOTargetConfigValidateRejectsBadExpected(t *testing.T) {
	config := GRPOTargetConfig{BoundaryReference: BoundaryReferenceSet{
		Items: []BoundaryReferenceItem{{Level: "a", Input: "x", Expected: "maybe"}},
	}}
	if err := config.Validate(); err == nil {
		t.Fatal("非法期望值必须被拒绝")
	}
	empty := GRPOTargetConfig{BoundaryReference: BoundaryReferenceSet{
		Items: []BoundaryReferenceItem{{Level: "a", Input: "", Expected: BoundaryExpectedAccept}},
	}}
	if err := empty.Validate(); err == nil {
		t.Fatal("空内容必须被拒绝")
	}
}

// TestLocalDimensionKeys 覆盖「确定性维度不属于任何裁判」的分派依据。
func TestLocalDimensionKeys(t *testing.T) {
	grpo := LocalDimensionKeys(TargetKindGRPO)
	if len(grpo) != 1 || grpo[0] != GRPODimLevelCoverage {
		t.Fatalf("GRPO 的确定性维度应只有 level_coverage，实际 %v", grpo)
	}
	if IsLocalDimension(TargetKindGRPO, GRPODimBoundaryStability) {
		t.Fatal("边界稳定性必须由模型裁判判定，不属于确定性维度")
	}
	if len(LocalDimensionKeys(TargetKindSFT)) != 0 {
		t.Fatal("SFT 没有确定性维度")
	}
}

// TestBuiltinGRPORubricWeightsSumToOne 覆盖量表自身的合法性。
func TestBuiltinGRPORubricWeightsSumToOne(t *testing.T) {
	rubric := BuiltinGRPORubric()
	if err := rubric.Validate(); err != nil {
		t.Fatalf("内置 GRPO 量表必须合法：%v", err)
	}
	if len(rubric.Dimensions) != 3 {
		t.Fatalf("GRPO 量表应有 3 个维度，实际 %d", len(rubric.Dimensions))
	}
}

// TestParseGRPOSamplePayloadRejectsInvalidJSON 覆盖「坏 JSON 是校验失败」。
func TestParseGRPOSamplePayloadRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseGRPOSamplePayload(json.RawMessage(`{`)); err == nil {
		t.Fatal("坏 JSON 必须被拒绝")
	}
	if _, err := ParseGRPOSamplePayload(nil); err == nil {
		t.Fatal("空 payload 必须被拒绝")
	}
}
