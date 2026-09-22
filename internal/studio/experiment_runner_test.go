package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T14 的**执行侧**（experiment runner）。
//
// 用可控假裁判而不是真实模型：验收项要求「缺分 / error / 不适用与真实 0 分
// 区分」，这三条必须能被按需驱动；真实裁判既不稳定也不该在测试里花钱。

// scriptedJudge 按维度脚本化的假裁判。
type scriptedJudge struct {
	mu           sync.Mutex
	perDimension map[string]JudgeVerdict
	failWithAll  error
	calls        int
	seenRubric   []model.RubricSpec
	seenPayloads []string
}

func (judge *scriptedJudge) JudgeItem(ctx context.Context, request JudgeRequest) ([]JudgeVerdict, error) {
	judge.mu.Lock()
	judge.calls++
	judge.seenRubric = append(judge.seenRubric, request.Rubric)
	judge.seenPayloads = append(judge.seenPayloads, string(request.Payload))
	judge.mu.Unlock()

	if judge.failWithAll != nil {
		return nil, judge.failWithAll
	}
	verdicts := []JudgeVerdict{}
	for _, dimension := range request.Rubric.Dimensions {
		if verdict, found := judge.perDimension[dimension.Key]; found {
			verdicts = append(verdicts, JudgeVerdict{
				Dimension: dimension.Key, RawScore: verdict.RawScore, State: verdict.State,
				Rationale: verdict.Rationale, ErrorClass: verdict.ErrorClass,
			})
			continue
		}
		score := 8.0
		verdicts = append(verdicts, JudgeVerdict{Dimension: dimension.Key, RawScore: &score, Rationale: "符合要求"})
	}
	return verdicts, nil
}

func (judge *scriptedJudge) callCount() int {
	judge.mu.Lock()
	defer judge.mu.Unlock()
	return judge.calls
}

type evalFixture struct {
	runner      *ExperimentRunner
	experiments *store.ExperimentStore
	pool        *pgxpool.Pool
	experiment  model.Experiment
	judge       *scriptedJudge
}

// newEvalFixture 复用 T12 的 runner fixture（工作区/用户/项目/覆盖），
// 再补一个批次与三个样本版本，最后建实验。
func newEvalFixture(t *testing.T, verdicts map[string]JudgeVerdict) evalFixture {
	t.Helper()
	base := newRunnerFixture(t, &scriptedGenerator{})
	ctx := context.Background()
	suffix := strconv.Itoa(len(t.Name())) + "-" + strings.ReplaceAll(t.Name(), "/", "_")

	// 用 json.Marshal 而不是手拼 JSON：手拼既可能格式错误，又会让静态分析器
	// 无法区分「参数值」与「被拼进 SQL 的文本」。
	generationConfig, err := json.Marshal(map[string]any{
		"modelConnectionId": 1, "concurrency": 1, "maxTokens": 512, "schemaVersion": "sft.sample.v1",
	})
	if err != nil {
		t.Fatalf("marshal generation config: %v", err)
	}
	var batchID int64
	if err := base.pool.QueryRow(ctx, `
    INSERT INTO batches (project_id, purpose, status, control_state, target_kind, schema_version,
                         generation_config, planned_units, budget_currency, created_by)
    VALUES ($1, 'pilot', 'completed', 'run', 'sft', 'sft.sample.v1', $2::jsonb, 3, 'CNY', $3)
    RETURNING id`, base.projectID, generationConfig, base.userID).Scan(&batchID); err != nil {
		t.Fatalf("seed batch: %v", err)
	}

	versionIDs := []int64{}
	for index := 1; index <= 3; index++ {
		var sampleID int64
		if err := base.pool.QueryRow(ctx, `
      INSERT INTO samples (project_id, sample_key, target_kind, title, latest_version)
      VALUES ($1, $2, 'sft', $2, 1) RETURNING id`,
			base.projectID, "eval-item-"+suffix+"-"+strconv.Itoa(index)).Scan(&sampleID); err != nil {
			t.Fatalf("seed sample: %v", err)
		}
		// 用手写 JSON 字符串会产生「参数值 vs 拼进 SQL 的文本」的静态分析噪声，
		// 因此统一用 json.Marshal 构造载荷。
		payload, marshalErr := json.Marshal(map[string]any{
			"question": "q" + strconv.Itoa(index), "reasoning": "r", "answer": "a",
		})
		if marshalErr != nil {
			t.Fatalf("marshal payload: %v", marshalErr)
		}
		var versionID int64
		if err := base.pool.QueryRow(ctx, `
      INSERT INTO sample_versions
        (sample_id, project_id, version, target_kind, schema_version, payload, content_hash, batch_id,
         generator_config)
      VALUES ($1, $2, 1, 'sft', 'sft.sample.v1', $3::jsonb, $4, $5, '{"modelConnectionId":1}'::jsonb)
      RETURNING id`,
			sampleID, base.projectID, payload,
			"eval-hash-"+suffix+"-"+strconv.Itoa(index), batchID).Scan(&versionID); err != nil {
			t.Fatalf("seed sample version: %v", err)
		}
		versionIDs = append(versionIDs, versionID)
	}

	experiments := store.NewExperimentStore(base.pool)
	judge := &scriptedJudge{perDimension: verdicts}
	experiment, err := experiments.CreateExperiment(ctx, store.CreateExperimentInput{
		ProjectID: base.projectID, BatchID: &batchID, TargetKind: model.TargetKindSFT,
		SampleVersionIDs: versionIDs,
		// 裁判连接与批次里的生成连接（id=1）不同源（99999），满足独立性。
		Judges: []model.JudgeSpec{{
			ConnectionID: 99999, Label: "独立裁判", EndpointFingerprint: "judge.example.com/v1",
		}},
		Rubric: model.RubricSpec{Dimensions: []model.RubricDimension{
			{Key: "accuracy", Label: "准确", Weight: 0.6, Min: 0, Max: 10},
			{Key: "reasoning", Label: "推理", Weight: 0.4, Min: 0, Max: 10},
		}},
		CreatedBy: &base.userID,
	})
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if experiment.InspectedCount != 3 {
		t.Fatalf("fixture 分母应为 3，实际 %d", experiment.InspectedCount)
	}

	return evalFixture{
		// Batches 必须注入：runner 靠它按**冻结的 sample_version_id** 读内容。
		runner:      &ExperimentRunner{Experiments: experiments, Batches: base.batches, Judge: judge},
		experiments: experiments, pool: base.pool, experiment: experiment, judge: judge,
	}
}

// TestRunExperimentScoresAllItems 覆盖正常路径（含「裁判收到冻结的量表与原文」）。
func TestRunExperimentScoresAllItems(t *testing.T) {
	fixture := newEvalFixture(t, nil)
	ctx := context.Background()

	result, err := fixture.runner.RunExperiment(ctx, fixture.experiment.ID)
	if err != nil {
		t.Fatalf("RunExperiment: %v", err)
	}
	if result.Inspected != 3 || result.Scored != 3 || result.Missing != 0 || result.Error != 0 {
		t.Fatalf("3 项都应评分完成，实际 %+v", result)
	}
	if result.Status != model.ExperimentStatusCompleted {
		t.Fatalf("全部评分后应为 completed，实际 %s", result.Status)
	}
	if fixture.judge.callCount() != 3 {
		t.Fatalf("每项应调用裁判一次，实际 %d 次", fixture.judge.callCount())
	}

	// 裁判必须收到**实验冻结的量表**与**不可变原文**。
	fixture.judge.mu.Lock()
	rubric := fixture.judge.seenRubric[0]
	payload := fixture.judge.seenPayloads[0]
	fixture.judge.mu.Unlock()
	if len(rubric.Dimensions) != 2 || rubric.Dimensions[0].Weight != 0.6 {
		t.Fatalf("裁判必须收到冻结的量表，实际 %+v", rubric.Dimensions)
	}
	if !strings.Contains(payload, `"question"`) {
		t.Fatalf("裁判必须收到样本原文，实际 %q", payload)
	}

	// 报告：均值来自 8/10=0.8，分母固定为 3。
	report, err := fixture.experiments.BuildExperimentReport(ctx, fixture.experiment.ID)
	if err != nil {
		t.Fatalf("BuildExperimentReport: %v", err)
	}
	if report.Stats.Inspected != 3 {
		t.Fatalf("报告分母必须为 3，实际 %d", report.Stats.Inspected)
	}
	for _, dimension := range report.Dimensions {
		if dimension.Mean < 0.79 || dimension.Mean > 0.81 {
			t.Fatalf("维度 %s 均值应为 0.8，实际 %.3f", dimension.Dimension, dimension.Mean)
		}
	}
}

// TestRunExperimentRecordsMissingAsMissingNotZero 覆盖验收项
// 「缺分/error/不适用不等于 0」。
func TestRunExperimentRecordsMissingAsMissingNotZero(t *testing.T) {
	fixture := newEvalFixture(t, map[string]JudgeVerdict{
		"reasoning": {Dimension: "reasoning", State: model.ScoreStateMissing, Rationale: "无法判断推理过程"},
	})
	ctx := context.Background()

	result, err := fixture.runner.RunExperiment(ctx, fixture.experiment.ID)
	if err != nil {
		t.Fatalf("RunExperiment: %v", err)
	}
	if result.Missing != 3 || result.Scored != 0 {
		t.Fatalf("3 项都缺 reasoning 分，实际 %+v", result)
	}
	// 有缺分不得声称 completed（否则就是「失败伪装成 completed 100%」）。
	if result.Status != model.ExperimentStatusPartialFailed {
		t.Fatalf("有缺分时应为 partial_failed，实际 %s", result.Status)
	}

	// 关键：缺分的格**没有分值**。有分值就会被聚合算成 0 分。
	var withScore int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM experiment_scores
    WHERE experiment_id = $1 AND dimension = 'reasoning' AND raw_score IS NOT NULL`,
		fixture.experiment.ID).Scan(&withScore); err != nil {
		t.Fatalf("count scores: %v", err)
	}
	if withScore != 0 {
		t.Fatalf("缺分不得带分值（否则会被算成 0 分），实际 %d 行带分值", withScore)
	}

	// 报告：缺分维度不计入覆盖，且分母不缩小。
	report, err := fixture.experiments.BuildExperimentReport(ctx, fixture.experiment.ID)
	if err != nil {
		t.Fatalf("BuildExperimentReport: %v", err)
	}
	if report.Stats.Inspected != 3 {
		t.Fatalf("缺分不缩小分母，实际 %d", report.Stats.Inspected)
	}
	for _, dimension := range report.Dimensions {
		if dimension.Dimension == "reasoning" && dimension.ScoredCount != 0 {
			t.Fatalf("缺分维度不得计入覆盖，实际 %d", dimension.ScoredCount)
		}
	}
}

// TestRunExperimentMarksJudgeFailureAsError 覆盖「裁判出错」与「缺分」区分。
func TestRunExperimentMarksJudgeFailureAsError(t *testing.T) {
	fixture := newEvalFixture(t, nil)
	fixture.judge.failWithAll = fmt.Errorf("upstream returned 503 service unavailable")
	ctx := context.Background()

	result, err := fixture.runner.RunExperiment(ctx, fixture.experiment.ID)
	if err != nil {
		t.Fatalf("RunExperiment: %v", err)
	}
	if result.Error != 3 || result.Scored != 0 {
		t.Fatalf("裁判整体失败应把 3 项都标成 error，实际 %+v", result)
	}

	// 每维度一行 error（而不是缺分行）：出错要重试，缺分说明该维度无法评价。
	var errorRows, missingRows int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FILTER (WHERE score_state = 'error'),
           COUNT(*) FILTER (WHERE score_state = 'missing')
    FROM experiment_scores WHERE experiment_id = $1`, fixture.experiment.ID,
	).Scan(&errorRows, &missingRows); err != nil {
		t.Fatalf("count score states: %v", err)
	}
	if errorRows != 6 || missingRows != 0 {
		t.Fatalf("3 项 × 2 维度都应记 error，实际 error=%d missing=%d", errorRows, missingRows)
	}
}

// TestRunExperimentResumeDoesNotRegradeScoredItems 覆盖验收项
// 「失败项续跑不覆盖成功证据」。
func TestRunExperimentResumeDoesNotRegradeScoredItems(t *testing.T) {
	fixture := newEvalFixture(t, nil)
	ctx := context.Background()

	if _, err := fixture.runner.RunExperiment(ctx, fixture.experiment.ID); err != nil {
		t.Fatalf("第一次 RunExperiment: %v", err)
	}
	firstCalls := fixture.judge.callCount()
	if firstCalls != 3 {
		t.Fatalf("第一次应评 3 项，实际 %d", firstCalls)
	}

	// 续跑：没有未完成项，不应再调用裁判（也不会重复花钱）。
	result, err := fixture.runner.RunExperiment(ctx, fixture.experiment.ID)
	if err != nil {
		t.Fatalf("续跑 RunExperiment: %v", err)
	}
	if fixture.judge.callCount() != firstCalls {
		t.Fatalf("续跑不得重新评分已完成项，实际新增 %d 次调用",
			fixture.judge.callCount()-firstCalls)
	}
	if result.Scored != 3 || result.Status != model.ExperimentStatusCompleted {
		t.Fatalf("续跑后应保持 3 项完成，实际 %+v", result)
	}
}

// TestRunExperimentRequiresLocalJudgeForGRPO 覆盖 T24 的执行侧接线检查。
//
// GRPO 不再被拒绝，但 runner 必须注入确定性判据实现（档位覆盖）。
// 缺失时**显式失败**：静默跳过会让该维度永远缺分，
// 而报告看起来只是「覆盖不足」—— 一个看起来正常的错误。
func TestRunExperimentRequiresLocalJudgeForGRPO(t *testing.T) {
	fixture := newEvalFixture(t, nil)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE experiments SET target_kind = 'grpo' WHERE id = $1`, fixture.experiment.ID); err != nil {
		t.Fatalf("switch target kind: %v", err)
	}
	_, err := fixture.runner.RunExperiment(ctx, fixture.experiment.ID)
	if err == nil || !strings.Contains(err.Error(), "确定性判据") {
		t.Fatalf("GRPO 实验缺少 LocalJudge 时必须显式失败，实际 %v", err)
	}
}

// TestJudgePromptForListsEveryDimension 覆盖提示词契约。
func TestJudgePromptForListsEveryDimension(t *testing.T) {
	prompt := JudgePromptFor(JudgeRequest{
		Payload: []byte(`{"question":"q"}`),
		Rubric: model.RubricSpec{Dimensions: []model.RubricDimension{
			{Key: "accuracy", Label: "准确", Weight: 0.6, Min: 0, Max: 10},
			{Key: "reasoning", Label: "推理", Weight: 0.4, Min: 0, Max: 10},
		}},
		GeneratorFingerprint: "api.vendor.com/v1",
	})
	for _, want := range []string{"accuracy", "reasoning", "0.00–10.00", "api.vendor.com/v1", "不要填 0"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("提示词必须包含 %q，实际：%s", want, prompt)
		}
	}
}
