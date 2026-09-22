package studio

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T24 在**执行侧**的分派：
// GRPO 的确定性维度（档位覆盖）只记一行，模型裁判只回答其余维度。
//
// 为什么这条必须测：档位覆盖如果也交给模型裁判，同一个维度会出现
// 「确定性判定」与「模型分」两个来源，而报告无法判断该信哪个；
// 反过来若确定性维度被静默跳过，它会永远缺分而报告看起来
// 只是「覆盖不足」—— 两种错法在界面上都不报错。

// grpoScriptedJudge 记录**被要求回答的维度**，并按维度给出分数。
type grpoScriptedJudge struct {
	mu           sync.Mutex
	calls        int
	seenJudged   [][]string
	seenPayloads []string
}

func (judge *grpoScriptedJudge) JudgeItem(_ context.Context, request JudgeRequest) ([]JudgeVerdict, error) {
	judge.mu.Lock()
	judge.calls++
	judge.seenJudged = append(judge.seenJudged, append([]string{}, request.JudgedDimensions...))
	judge.seenPayloads = append(judge.seenPayloads, string(request.Payload))
	judge.mu.Unlock()

	verdicts := make([]JudgeVerdict, 0, len(request.JudgedDimensions))
	for _, dimension := range request.JudgedDimensions {
		score := 4.0
		verdicts = append(verdicts, JudgeVerdict{
			Dimension: dimension, RawScore: &score, State: model.ScoreStateScored,
			Rationale: "测试裁判给出的分",
		})
	}
	return verdicts, nil
}

// grpoLocalJudgeStub 是一个可控的确定性判据实现。
type grpoLocalJudgeStub struct {
	verdicts []JudgeVerdict
}

func (stub grpoLocalJudgeStub) LocalVerdicts(_ context.Context, _ JudgeRequest) ([]JudgeVerdict, error) {
	return stub.verdicts, nil
}

type grpoEvalFixture struct {
	runner      *ExperimentRunner
	experiments *store.ExperimentStore
	pool        *pgxpool.Pool
	experiment  model.Experiment
	judge       *grpoScriptedJudge
}

// newGRPOEvalFixture 建一个 GRPO 实验：一个批次 + 一个 GRPO 样本版本。
func newGRPOEvalFixture(t *testing.T, local LocalJudge) grpoEvalFixture {
	t.Helper()
	base := newRunnerFixture(t, &scriptedGenerator{})
	ctx := context.Background()
	suffix := strconv.Itoa(len(t.Name())) + "-" + strings.ReplaceAll(t.Name(), "/", "_")

	generationConfig, err := json.Marshal(map[string]any{
		"modelConnectionId": 1, "concurrency": 1, "maxTokens": 512, "schemaVersion": "grpo.sample.v1",
	})
	if err != nil {
		t.Fatalf("marshal generation config: %v", err)
	}
	var batchID int64
	if err := base.pool.QueryRow(ctx, `
    INSERT INTO batches (project_id, purpose, status, control_state, target_kind, schema_version,
                         generation_config, planned_units, budget_currency, created_by)
    VALUES ($1, 'pilot', 'completed', 'run', 'grpo', 'grpo.sample.v1', $2::jsonb, 1, 'CNY', $3)
    RETURNING id`, base.projectID, generationConfig, base.userID).Scan(&batchID); err != nil {
		t.Fatalf("seed grpo batch: %v", err)
	}

	samplePayload, err := json.Marshal(map[string]any{
		"question":    "为什么快速排序最坏退化",
		"judgePrompt": "按档位评分：基础、精通。",
		"levels":      []string{"基础", "精通"},
		"levelRubrics": []map[string]any{
			{"level": "基础", "criteria": "能说出基准", "acceptCase": "提到 pivot", "rejectCase": "答非所问"},
			{"level": "精通", "criteria": "能讨论退化", "acceptCase": "指出有序输入", "rejectCase": "否认退化"},
		},
		"frameworkRef": "teacher-v1",
	})
	if err != nil {
		t.Fatalf("marshal grpo payload: %v", err)
	}

	var sampleID, versionID int64
	if err := base.pool.QueryRow(ctx, `
    INSERT INTO samples (project_id, sample_key, target_kind, title, latest_version)
    VALUES ($1, $2, 'grpo', $2, 1) RETURNING id`,
		base.projectID, "grpo-item-"+suffix).Scan(&sampleID); err != nil {
		t.Fatalf("seed grpo sample: %v", err)
	}
	if err := base.pool.QueryRow(ctx, `
    INSERT INTO sample_versions
      (sample_id, project_id, version, target_kind, schema_version, payload, content_hash, batch_id,
       generator_config)
    VALUES ($1, $2, 1, 'grpo', 'grpo.sample.v1', $3::jsonb, $4, $5, '{"modelConnectionId":1}'::jsonb)
    RETURNING id`,
		sampleID, base.projectID, samplePayload, "grpo-hash-"+suffix, batchID).Scan(&versionID); err != nil {
		t.Fatalf("seed grpo sample version: %v", err)
	}

	targetConfig, err := json.Marshal(model.GRPOTargetConfig{
		TeacherPromptVersion: "teacher-v1",
		BoundaryReference: model.BoundaryReferenceSet{
			ID: "br-1", Source: "人工标注",
			Items: []model.BoundaryReferenceItem{
				{Level: "基础", Input: "只说了要分区", Expected: model.BoundaryExpectedAccept},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal target config: %v", err)
	}

	experiments := store.NewExperimentStore(base.pool)
	judge := &grpoScriptedJudge{}
	experiment, err := experiments.CreateExperiment(ctx, store.CreateExperimentInput{
		ProjectID: base.projectID, BatchID: &batchID, TargetKind: model.TargetKindGRPO,
		SampleVersionIDs: []int64{versionID},
		// 裁判与生成连接（id=1）不同源，满足独立性检查。
		Judges: []model.JudgeSpec{{
			ConnectionID: 99999, Label: "独立裁判", EndpointFingerprint: "judge.example.com/v1",
		}},
		Rubric:       model.BuiltinGRPORubric(),
		TargetConfig: targetConfig,
		CreatedBy:    &base.userID,
	})
	if err != nil {
		t.Fatalf("CreateExperiment(grpo): %v", err)
	}

	return grpoEvalFixture{
		runner: &ExperimentRunner{
			Experiments: experiments, Batches: base.batches, Judge: judge, Local: local,
		},
		experiments: experiments, pool: base.pool, experiment: experiment, judge: judge,
	}
}

// TestRunGRPOExperimentSeparatesLocalAndJudgeDimensions 覆盖
// 「确定性维度只记一行，模型裁判只回答其余维度」。
func TestRunGRPOExperimentSeparatesLocalAndJudgeDimensions(t *testing.T) {
	coverage := 1.0
	fixture := newGRPOEvalFixture(t, grpoLocalJudgeStub{verdicts: []JudgeVerdict{{
		Dimension: model.GRPODimLevelCoverage, RawScore: &coverage,
		State: model.ScoreStateScored, Rationale: "档位覆盖 2/2",
	}}})

	result, err := fixture.runner.RunExperiment(context.Background(), fixture.experiment.ID)
	if err != nil {
		t.Fatalf("RunExperiment(grpo): %v", err)
	}
	if result.Status != model.ExperimentStatusCompleted {
		t.Fatalf("确定性维度 + 两个裁判维度都有分时应为 completed，实际 %s（%+v）", result.Status, result)
	}

	// 模型裁判只应被要求回答两个非确定性维度。
	if fixture.judge.calls != 1 {
		t.Fatalf("应调用一次裁判，实际 %d 次", fixture.judge.calls)
	}
	judged := fixture.judge.seenJudged[0]
	if len(judged) != 2 {
		t.Fatalf("裁判应只回答两个维度，实际 %v（确定性维度不得交给模型）", judged)
	}
	for _, key := range judged {
		if key == model.GRPODimLevelCoverage {
			t.Fatal("档位覆盖是确定性维度，不得要求模型裁判回答")
		}
	}

	// 确定性维度必须落到 judge_connection_id = 0 的一行（只记一行，
	// 不按裁判数复制 —— 复制 N 份会显示「N 名裁判完全一致」的假一致）。
	var localRows int
	if err := fixture.pool.QueryRow(context.Background(), `
    SELECT COUNT(*) FROM experiment_scores
    WHERE experiment_id = $1 AND dimension = $2 AND judge_connection_id = 0
      AND superseded_by IS NULL`, fixture.experiment.ID, model.GRPODimLevelCoverage).Scan(&localRows); err != nil {
		t.Fatalf("query local dimension rows: %v", err)
	}
	if localRows != 1 {
		t.Fatalf("确定性维度应只记一行（judge_connection_id = 0），实际 %d 行", localRows)
	}
}

// TestRunGRPOExperimentFailsWithoutLocalJudge 覆盖「缺确定性判据实现必须显式失败」。
//
// 静默跳过会让确定性维度永远缺分，而报告看起来只是「覆盖不足」。
func TestRunGRPOExperimentFailsWithoutLocalJudge(t *testing.T) {
	fixture := newGRPOEvalFixture(t, nil)
	_, err := fixture.runner.RunExperiment(context.Background(), fixture.experiment.ID)
	if err == nil {
		t.Fatal("量表含确定性维度但 runner 未注入 LocalJudge 时必须失败")
	}
	if !strings.Contains(err.Error(), "确定性判据") {
		t.Fatalf("错误必须点明缺少确定性判据实现，实际 %v", err)
	}
}
