package eval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
)

// 本文件验证 Issue #160 T24 的 GRPO 质量适配器。
//
// 全部是**纯函数**级别的断言（提示词构造、逐条比对、缺分语义）：
// 真正的模型调用在测试里既不稳定也会花钱，而 T24 的三条核心主张
//（不复用 SFT 打分、缺分不是 0、逐条可追溯）都不依赖模型。

func grpoSampleForTest() model.GRPOSample {
	return model.GRPOSample{
		Question:    "解释一下快速排序的分区策略",
		JudgePrompt: "请按档位评分：档位一 基础、档位二 熟练、档位三 精通。",
		Levels:      []string{"档位一 基础", "档位二 熟练", "档位三 精通"},
		LevelRubrics: []model.GrpoLevelRubric{
			{Level: "档位一 基础", Criteria: "能说出分区思想", AcceptCase: "提到选择基准", RejectCase: "答非所问"},
			{Level: "档位二 熟练", Criteria: "能说明复杂度", AcceptCase: "给出 O(n log n)", RejectCase: "复杂度写错"},
			{Level: "档位三 精通", Criteria: "能讨论最坏情况", AcceptCase: "指出有序输入退化", RejectCase: "否认退化"},
		},
		FrameworkRef: "teacher-v3",
	}
}

// TestGRPOJudgeUserPromptDoesNotAskForSFTFields 覆盖 T24
// 「不能仅对空 reasoning/answer 调用 SFT scorer」。
//
// 断言的是提示词里**没有** reasoning/answer 的措辞：GRPO 样本里根本没有
// 这两个字段，要求模型评价它们只会得到一段编造的解释。
func TestGRPOJudgeUserPromptDoesNotAskForSFTFields(t *testing.T) {
	request := GRPOJudgeRequest{
		Sample:     grpoSampleForTest(),
		Dimensions: model.GRPOJudgedDimensionKeys(),
	}
	prompt, err := GRPOJudgeUserPrompt(request)
	if err != nil {
		t.Fatalf("构造提示词失败：%v", err)
	}
	if strings.Contains(prompt, "reasoning") {
		t.Fatal("GRPO 提示词不得要求评价 reasoning（该字段在 GRPO 样本里不存在）")
	}
	if strings.Contains(prompt, "【答案】") {
		t.Fatal("GRPO 提示词不得要求评价答案")
	}
	for _, level := range request.Sample.Levels {
		if !strings.Contains(prompt, level) {
			t.Fatalf("提示词必须列出全部档位，缺少 %q", level)
		}
	}
	if !strings.Contains(prompt, "explanationConsistency") {
		t.Fatal("提示词必须要求评分解释一致性维度")
	}
}

// TestGRPOJudgeUserPromptIncludesIndexedBoundaryItems 覆盖
// 「相同边界样例的重复/不同裁判差异可追溯」：参考样例必须带下标出现，
// 否则裁判的逐条回答无法对应到具体样例。
func TestGRPOJudgeUserPromptIncludesIndexedBoundaryItems(t *testing.T) {
	request := GRPOJudgeRequest{
		Sample:     grpoSampleForTest(),
		Dimensions: model.GRPOJudgedDimensionKeys(),
		Config: model.GRPOTargetConfig{BoundaryReference: model.BoundaryReferenceSet{
			ID: "br-1", Source: "人工标注", Sampled: true,
			Items: []model.BoundaryReferenceItem{
				{Level: "档位一 基础", Input: "只说了要分区", Expected: model.BoundaryExpectedAccept},
				{Level: "档位三 精通", Input: "复杂度 O(n)", Expected: model.BoundaryExpectedReject},
			},
		}},
	}
	prompt, err := GRPOJudgeUserPrompt(request)
	if err != nil {
		t.Fatalf("构造提示词失败：%v", err)
	}
	if !strings.Contains(prompt, "[0]") || !strings.Contains(prompt, "[1]") {
		t.Fatal("边界样例必须带下标，否则逐条差异无法追溯")
	}
	if !strings.Contains(prompt, "只说了要分区") {
		t.Fatal("边界样例内容必须出现在提示词里")
	}
}

// TestBoundaryStabilityVerdictScoresMatchesAndKeepsDetail 覆盖
// 「逐条判定 → 可复算分数 + 可追溯理由」。
func TestBoundaryStabilityVerdictScoresMatchesAndKeepsDetail(t *testing.T) {
	request := GRPOJudgeRequest{
		Sample: grpoSampleForTest(),
		Config: model.GRPOTargetConfig{BoundaryReference: model.BoundaryReferenceSet{
			Items: []model.BoundaryReferenceItem{
				{Level: "档位一 基础", Input: "a", Expected: model.BoundaryExpectedAccept},
				{Level: "档位三 精通", Input: "b", Expected: model.BoundaryExpectedReject},
			},
		}},
	}
	payload := grpoJudgePayload{}
	payload.Boundary = append(payload.Boundary,
		struct {
			Index     int    `json:"index"`
			Verdict   string `json:"verdict"`
			Rationale string `json:"rationale"`
		}{Index: 0, Verdict: grpoBoundaryAccept, Rationale: "边界清晰"})
	payload.Boundary = append(payload.Boundary,
		struct {
			Index     int    `json:"index"`
			Verdict   string `json:"verdict"`
			Rationale string `json:"rationale"`
		}{Index: 1, Verdict: grpoBoundaryAccept, Rationale: "判据没写清退化"})

	verdict := boundaryStabilityVerdict(request, payload)
	if verdict.State != model.ScoreStateScored || verdict.RawScore == nil {
		t.Fatalf("有参考集时应给出分数，实际 %+v", verdict)
	}
	// 1 条一致 / 2 条 → 1 + 4*(1/2) = 3 分。
	if *verdict.RawScore != 3 {
		t.Fatalf("一致 1/2 应得 3 分，实际 %v", *verdict.RawScore)
	}
	if !strings.Contains(verdict.Rationale, "#1") {
		t.Fatalf("理由必须指出是哪一条不一致，实际 %q", verdict.Rationale)
	}
}

// TestExplanationConsistencyMissingIsNotZero 覆盖 §2.6「缺分不是 0」。
func TestExplanationConsistencyMissingIsNotZero(t *testing.T) {
	verdict := explanationConsistencyVerdict(grpoJudgePayload{})
	if verdict.State != model.ScoreStateMissing {
		t.Fatalf("裁判未给分必须记 missing，实际 %s", verdict.State)
	}
	if verdict.RawScore != nil {
		t.Fatal("缺分不得带分数（补 0 会把「没评」算成最差分）")
	}
}

// TestExplanationConsistencyRejectsOutOfRange 覆盖「不夹紧」原则：
// 裁判没按量表回答是事实，记缺分而不是把它夹到合法区间。
func TestExplanationConsistencyRejectsOutOfRange(t *testing.T) {
	score := 9.0
	verdict := explanationConsistencyVerdict(grpoJudgePayload{
		ExplanationConsistency: struct {
			Score     *float64 `json:"score"`
			Rationale string   `json:"rationale"`
		}{Score: &score, Rationale: "给高了"},
	})
	if verdict.State != model.ScoreStateMissing || verdict.RawScore != nil {
		t.Fatalf("越界分数必须记缺分，实际 %+v", verdict)
	}
}

// TestJudgeGRPOItemWithoutBoundaryReferenceSkipsModel 覆盖 T24
// 「缺参考样例显示缺证据，不生成假统计」——并且**不调用模型**。
//
// 用一个必然不可用的 provider 配置：只要函数尝试调用模型就会失败，
// 因此「没有报错且返回 missing」同时证明了它没有发起调用。
func TestJudgeGRPOItemWithoutBoundaryReferenceSkipsModel(t *testing.T) {
	verdicts, err := JudgeGRPOItem(context.Background(), llm.ProviderConfig{
		BaseURL: "http://127.0.0.1:1", Model: "never-called", ProviderType: "openai", APIKey: "x",
	}, GRPOJudgeRequest{
		Sample:     grpoSampleForTest(),
		Dimensions: []string{model.GRPODimBoundaryStability},
	}, time.Millisecond)
	if err != nil {
		t.Fatalf("没有参考集时不应调用模型（也不应报错），实际 %v", err)
	}
	if len(verdicts) != 1 {
		t.Fatalf("应返回一条缺分判定，实际 %d 条", len(verdicts))
	}
	if verdicts[0].State != model.ScoreStateMissing {
		t.Fatalf("缺参考集必须记缺分，实际 %s", verdicts[0].State)
	}
	if verdicts[0].RawScore != nil {
		t.Fatal("缺参考集不得给出分数")
	}
}
