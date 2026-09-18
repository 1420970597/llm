package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// fakeScoreJSON 替换 scoreJSONFunc，记录每次调用的提示词并返回预置结果。
//
// 返回一个还原函数，测试必须 defer 调用它，避免污染同包其他测试。
func fakeScoreJSON(reply func(system, user string) (ScoreResponse, error)) func() {
	original := scoreJSONFunc
	scoreJSONFunc = func(_ context.Context, judge JudgeRef, system, user string, target any, _ time.Duration) error {
		response, err := reply(system, user)
		if err != nil {
			return err
		}
		// 与 llm.CompleteStructured 的语义一致：把解析结果写进 target。
		pointer, ok := target.(*ScoreResponse)
		if !ok {
			return fmt.Errorf("fake judge got unexpected target type %T", target)
		}
		*pointer = response
		return nil
	}
	return func() { scoreJSONFunc = original }
}

func testDimension() model.EvalDimension {
	return model.EvalDimension{
		Key:         "long_chain.depth",
		Name:        "思维链深度",
		Category:    "long_chain",
		Description: "衡量推理是否逐步深入到问题本质",
		Rubric:      "9~10 分：每一步都不可省略，且后一步依赖前一步的结论；0~3 分：只有结论罗列。",
		ScaleMin:    0,
		ScaleMax:    10,
		Weight:      1,
		IsActive:    true,
	}
}

func testJudge(id int64) JudgeRef {
	return JudgeRef{ProviderID: id, ProviderName: fmt.Sprintf("judge-%d", id), Model: "test-model"}
}

// 提示词必须包含 rubric 与 scale 区间，否则裁判无从按标准打分。
func TestBuildScorePromptsIncludesRubricAndScale(t *testing.T) {
	dimension := testDimension()
	system, user := BuildScorePrompts(dimension, "问题正文", "思维链正文", "答案正文")

	if !strings.Contains(system, `{"score": <数字>, "rationale": "<中文评分理由>"}`) {
		t.Error("system prompt must pin the exact JSON shape the judge has to return")
	}
	if !strings.Contains(user, dimension.Rubric) {
		t.Error("user prompt must carry the dimension rubric verbatim")
	}
	if !strings.Contains(user, "0 分（最低）~ 10 分（最高）") {
		t.Errorf("user prompt must state the scale range, got:\n%s", user)
	}
	if !strings.Contains(user, dimension.Name) {
		t.Error("user prompt must name the dimension")
	}
	if !strings.Contains(user, dimension.Description) {
		t.Error("user prompt must carry the dimension description")
	}

	for _, fragment := range []string{"问题正文", "思维链正文", "答案正文"} {
		if !strings.Contains(user, fragment) {
			t.Errorf("user prompt must include the evaluated content %q", fragment)
		}
	}
}

func TestBuildScorePromptsMarksEmptyFields(t *testing.T) {
	_, user := BuildScorePrompts(testDimension(), "有问题的正文", "   ", "")

	if !strings.Contains(user, "（该项为空）") {
		t.Error("empty reasoning/answer must be marked explicitly so the judge does not read it as a prompt bug")
	}
	if strings.Count(user, "（该项为空）") != 2 {
		t.Errorf("expected both empty fields to be marked, got %d marker(s)", strings.Count(user, "（该项为空）"))
	}
}

func TestBuildScorePromptsUsesCustomScale(t *testing.T) {
	dimension := testDimension()
	dimension.ScaleMin = 1
	dimension.ScaleMax = 5

	system, user := BuildScorePrompts(dimension, "q", "r", "a")

	if !strings.Contains(system, "1 到 5") {
		t.Errorf("system prompt must use the dimension's own scale, got:\n%s", system)
	}
	if !strings.Contains(user, "1 分（最低）~ 5 分（最高）") {
		t.Errorf("user prompt must use the dimension's own scale, got:\n%s", user)
	}
}

func TestClampScore(t *testing.T) {
	cases := []struct {
		name      string
		score     float64
		min, max  int
		want      float64
		wantClamp bool
	}{
		{"in range", 7, 0, 10, 7, false},
		{"at lower bound", 0, 0, 10, 0, false},
		{"at upper bound", 10, 0, 10, 10, false},
		{"above range", 15, 0, 10, 10, true},
		{"below range", -3, 0, 10, 0, true},
		{"custom scale above", 6, 1, 5, 5, true},
		{"custom scale below", 0, 1, 5, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := ClampScore(tc.score, tc.min, tc.max)
			if got != tc.want {
				t.Errorf("score %v in [%d,%d]: want %v got %v", tc.score, tc.min, tc.max, tc.want, got)
			}
			if clamped != tc.wantClamp {
				t.Errorf("score %v in [%d,%d]: want clamped=%v got %v", tc.score, tc.min, tc.max, tc.wantClamp, clamped)
			}
		})
	}
}

// NaN 会污染均值与标准差，必须被夹紧到区间下界而不是透传。
func TestClampScoreHandlesNaN(t *testing.T) {
	got, clamped := ClampScore(nan(), 0, 10)
	if clamped != true {
		t.Error("NaN must be reported as clamped")
	}
	if got != 0 {
		t.Errorf("NaN should clamp to the scale minimum, got %v", got)
	}
}

func nan() float64 {
	zero := 0.0
	return zero / zero
}

// 越界分数必须被夹紧并记录，不得静默丢弃这条评分。
func TestScoreDimensionClampsOutOfRangeScore(t *testing.T) {
	defer fakeScoreJSON(func(_, _ string) (ScoreResponse, error) {
		return ScoreResponse{Score: 15, Rationale: "推理链极其完整"}, nil
	})()

	outcome := ScoreDimension(context.Background(), ScoreRequest{
		Judge:     testJudge(2),
		Dimension: testDimension(),
		Question:  "q", Reasoning: "r", Answer: "a",
	})

	if outcome.Status != ScoreStatusScored {
		t.Fatalf("out-of-range score must still be recorded, got status %q (err=%v)", outcome.Status, outcome.Err)
	}
	if outcome.Score != 10 {
		t.Errorf("score 15 must clamp to 10, got %v", outcome.Score)
	}
	if !outcome.Clamped {
		t.Error("outcome must flag that clamping happened")
	}
	if outcome.RawScore != 15 {
		t.Errorf("raw score must be preserved for audit, got %v", outcome.RawScore)
	}
	if !strings.Contains(outcome.Rationale, "15") {
		t.Errorf("rationale must record the original value, got %q", outcome.Rationale)
	}
	if !strings.Contains(outcome.Rationale, "推理链极其完整") {
		t.Errorf("rationale must keep the judge's own reasoning, got %q", outcome.Rationale)
	}
}

func TestScoreDimensionPassesThroughInRangeScore(t *testing.T) {
	defer fakeScoreJSON(func(_, _ string) (ScoreResponse, error) {
		return ScoreResponse{Score: 8, Rationale: "结构清晰"}, nil
	})()

	outcome := ScoreDimension(context.Background(), ScoreRequest{
		Judge: testJudge(2), Dimension: testDimension(),
	})

	if outcome.Status != ScoreStatusScored {
		t.Fatalf("expected scored, got %q (err=%v)", outcome.Status, outcome.Err)
	}
	if outcome.Score != 8 || outcome.Clamped {
		t.Errorf("in-range score must pass through unchanged, got %v clamped=%v", outcome.Score, outcome.Clamped)
	}
}

func TestScoreDimensionReportsJudgeFailure(t *testing.T) {
	defer fakeScoreJSON(func(_, _ string) (ScoreResponse, error) {
		return ScoreResponse{}, errors.New("judge timed out")
	})()

	outcome := ScoreDimension(context.Background(), ScoreRequest{
		Judge: testJudge(2), Dimension: testDimension(),
	})

	if outcome.Status != ScoreStatusFailed {
		t.Fatalf("expected failed status, got %q", outcome.Status)
	}
	if outcome.Err == nil {
		t.Fatal("failure must carry the underlying error")
	}
	if !strings.Contains(outcome.Err.Error(), "judge timed out") {
		t.Errorf("error must wrap the judge failure, got %q", outcome.Err.Error())
	}
	if !strings.Contains(outcome.Err.Error(), "long_chain.depth") {
		t.Errorf("error must name the dimension so failures are traceable, got %q", outcome.Err.Error())
	}
}

func TestScoreDimensionRejectsMissingInputs(t *testing.T) {
	// 不替换 fake：这两个分支必须在调裁判之前就返回，
	// 否则会真的打网络（用 provider id 0 与空 key 构造非法请求）。
	cases := []struct {
		name    string
		request ScoreRequest
	}{
		{"missing judge provider", ScoreRequest{Dimension: testDimension()}},
		{"missing dimension key", ScoreRequest{Judge: testJudge(2), Dimension: model.EvalDimension{Name: "无 key"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := ScoreDimension(context.Background(), tc.request)
			if outcome.Status != ScoreStatusFailed {
				t.Errorf("expected failed status, got %q", outcome.Status)
			}
			if outcome.Err == nil {
				t.Error("expected an error describing the missing input")
			}
		})
	}
}

func TestScoreDimensionMarksMissingRationale(t *testing.T) {
	defer fakeScoreJSON(func(_, _ string) (ScoreResponse, error) {
		return ScoreResponse{Score: 5, Rationale: "   "}, nil
	})()

	outcome := ScoreDimension(context.Background(), ScoreRequest{
		Judge: testJudge(2), Dimension: testDimension(),
	})

	if strings.TrimSpace(outcome.Rationale) == "" {
		t.Error("an empty judge rationale must be replaced with a visible marker, not stored as blank")
	}
}

// 失败隔离：某裁判连续失败不能让其余维度/裁判的评分丢失。
func TestScoreItemDimensionsIsolatesSingleFailure(t *testing.T) {
	defer fakeScoreJSON(func(_, user string) (ScoreResponse, error) {
		if strings.Contains(user, "会失败的维度") {
			return ScoreResponse{}, errors.New("judge exploded")
		}
		return ScoreResponse{Score: 6, Rationale: "正常"}, nil
	})()

	goodDimension := testDimension()
	badDimension := testDimension()
	badDimension.Key = "long_chain.broken"
	badDimension.Name = "会失败的维度"

	results := ScoreItemDimensions(context.Background(), ItemScoreRequest{
		Judges:     []JudgeRef{testJudge(2)},
		Dimensions: []model.EvalDimension{goodDimension, badDimension},
		Question:   "q", Reasoning: "r", Answer: "a",
	})

	if len(results) != 2 {
		t.Fatalf("a failing dimension must not abort the loop; expected 2 results, got %d", len(results))
	}
	if results[0].Outcome.Status != ScoreStatusScored {
		t.Errorf("first dimension should have succeeded, got %q", results[0].Outcome.Status)
	}
	if results[1].Outcome.Status != ScoreStatusFailed {
		t.Errorf("second dimension should have failed, got %q", results[1].Outcome.Status)
	}
}

// 一个裁判全失败时，另一个裁判的分数必须完整保留。
func TestScoreItemDimensionsIsolatesJudgeFailure(t *testing.T) {
	defer fakeScoreJSON(func(_ string, _ string) (ScoreResponse, error) {
		return ScoreResponse{Score: 4, Rationale: "ok"}, nil
	})()

	failing := testJudge(2)
	working := testJudge(3)

	original := scoreJSONFunc
	scoreJSONFunc = func(_ context.Context, judge JudgeRef, _, _ string, target any, _ time.Duration) error {
		if judge.ProviderID == failing.ProviderID {
			return errors.New("judge 2 unavailable")
		}
		pointer, ok := target.(*ScoreResponse)
		if !ok {
			return fmt.Errorf("unexpected target type %T", target)
		}
		*pointer = ScoreResponse{Score: 9, Rationale: "很好"}
		return nil
	}
	defer func() { scoreJSONFunc = original }()

	results := ScoreItemDimensions(context.Background(), ItemScoreRequest{
		Judges:     []JudgeRef{failing, working},
		Dimensions: []model.EvalDimension{testDimension()},
	})

	if len(results) != 2 {
		t.Fatalf("expected one result per judge, got %d", len(results))
	}
	if results[0].JudgeProviderID != failing.ProviderID || results[0].Outcome.Status != ScoreStatusFailed {
		t.Errorf("failing judge should be recorded as failed, got %+v", results[0])
	}
	if results[1].JudgeProviderID != working.ProviderID || results[1].Outcome.Status != ScoreStatusScored {
		t.Errorf("working judge must still be scored, got %+v", results[1])
	}
	if results[1].Outcome.Score != 9 {
		t.Errorf("working judge score must be preserved, got %v", results[1].Outcome.Score)
	}
}

// 结果键必须与 eval_item_scores 的 UNIQUE(eval_item_id, judge_provider_id, dimension_key)
// 对齐，调用方才能直接 upsert。
func TestScoreItemDimensionsKeyLayout(t *testing.T) {
	defer fakeScoreJSON(func(_, _ string) (ScoreResponse, error) {
		return ScoreResponse{Score: 5, Rationale: "ok"}, nil
	})()

	second := testDimension()
	second.Key = "faithfulness.grounded"

	results := ScoreItemDimensions(context.Background(), ItemScoreRequest{
		Judges:     []JudgeRef{testJudge(2), testJudge(3)},
		Dimensions: []model.EvalDimension{testDimension(), second},
	})

	if len(results) != 4 {
		t.Fatalf("expected 2 judges x 2 dimensions = 4 results, got %d", len(results))
	}

	// 顺序固定为「裁判顺序 × 维度顺序」，让报告可复现。
	wantOrder := []struct {
		providerID int64
		key        string
	}{
		{2, "long_chain.depth"},
		{2, "faithfulness.grounded"},
		{3, "long_chain.depth"},
		{3, "faithfulness.grounded"},
	}
	for index, want := range wantOrder {
		got := results[index]
		if got.JudgeProviderID != want.providerID || got.DimensionKey != want.key {
			t.Errorf("result %d: want judge %d / %s, got judge %d / %s",
				index, want.providerID, want.key, got.JudgeProviderID, got.DimensionKey)
		}
	}
}
