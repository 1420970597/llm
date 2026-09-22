package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T18 的比较基准与配对观测。
//
// 必须连真实 Postgres：核心断言是「同一题的两侧评分被**按单元键**对齐」，
// 而那完全依赖数据库里的 sample_key 与评分行。

type comparisonFixture struct {
	pool        *pgxpool.Pool
	projectID   int64
	userID      int64
	leftBatch   int64
	rightBatch  int64
	comparisons *ComparisonStore
	batches     *BatchStore
	reviews     *ReviewStore
}

// newComparisonFixture 建项目 + 两个批次，两侧各有 3 个**同名单元键**的样本版本。
//
// 单元键刻意相同（`cold-chain/temperature#n`）：那是「同一题在两方案下」
// 的对齐依据，而 T12 的覆盖分配正让两侧基于同一份覆盖时得到相同的键空间。
func newComparisonFixture(t *testing.T) comparisonFixture {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d-%s", os.Getpid(), strings.ReplaceAll(t.Name(), "/", "_"))

	var workspaceID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2)
    ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
		"比较测试工作区 "+suffix, "comparison-test-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user')
    ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role RETURNING id`,
		"comparison-"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	projects := NewProjectStore(pool)
	input := model.CreateProjectInput{Name: "比较项目", TargetKind: model.TargetKindSFT}
	input.Normalize()
	project, err := projects.CreateProject(ctx, workspaceID, userID, input)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	generationConfig, err := json.Marshal(map[string]any{
		"modelConnectionId": 1, "concurrency": 1, "maxTokens": 512, "schemaVersion": "sft.sample.v1",
	})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	createBatch := func(label string) int64 {
		var batchID int64
		if err := pool.QueryRow(ctx, `
      INSERT INTO batches (project_id, purpose, status, control_state, target_kind, schema_version,
                           generation_config, planned_units, budget_currency, created_by)
      VALUES ($1, 'pilot', 'completed', 'run', 'sft', 'sft.sample.v1', $2::jsonb, 3, 'CNY', $3)
      RETURNING id`, project.ID, generationConfig, userID).Scan(&batchID); err != nil {
			t.Fatalf("seed batch %s: %v", label, err)
		}
		return batchID
	}
	leftBatch := createBatch("left")
	rightBatch := createBatch("right")

	batches := NewBatchStore(pool)
	// 两侧各 3 个单元，**单元键相同**（同一题在两个方案下）。
	for ordinal := 1; ordinal <= 3; ordinal++ {
		itemKey := fmt.Sprintf("cold-chain/temperature#%d", ordinal)
		for _, batchID := range []int64{leftBatch, rightBatch} {
			sample, err := batches.EnsureSample(ctx, project.ID, itemKey, model.TargetKindSFT, itemKey, &batchID)
			if err != nil {
				t.Fatalf("EnsureSample: %v", err)
			}
			if _, _, err := batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
				ProjectID: project.ID, SampleKey: sample.SampleKey, TargetKind: model.TargetKindSFT,
				Title: itemKey, BatchID: &batchID,
				Payload: map[string]any{"question": itemKey, "reasoning": "r", "answer": "a"},
			}); err != nil {
				t.Fatalf("AppendSampleVersion: %v", err)
			}
		}
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM comparison_adoptions WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM project_adopted_batches WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM comparison_baselines WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM review_decisions WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM review_projections WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM experiments WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM projects WHERE workspace_id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})

	return comparisonFixture{
		pool: pool, projectID: project.ID, userID: userID,
		leftBatch: leftBatch, rightBatch: rightBatch,
		comparisons: NewComparisonStore(pool), batches: batches, reviews: NewReviewStore(pool),
	}
}

func comparisonRubric() model.RubricSpec {
	return model.RubricSpec{Dimensions: []model.RubricDimension{
		{Key: "accuracy", Label: "准确", Weight: 1, Min: 0, Max: 10},
	}}
}

// scoreBatch 给某个批次的每个单元写入一条评分（模拟该批次的实验已完成）。
//
// leftScores/rightScores 按单元序号给出原始分；nil 表示该单元缺分。
func (fixture comparisonFixture) scoreBatch(t *testing.T, batchID int64, scores []*float64) {
	t.Helper()
	ctx := context.Background()
	experiments := NewExperimentStore(fixture.pool)
	batches := NewBatchStore(fixture.pool)

	versionIDs := []int64{}
	for ordinal := 1; ordinal <= len(scores); ordinal++ {
		sampleKey := fmt.Sprintf("cold-chain/temperature#%d", ordinal)
		var sampleID, versionID int64
		if err := fixture.pool.QueryRow(ctx, `
      SELECT s.id, sv.id FROM samples s
      JOIN sample_versions sv ON sv.sample_id = s.id
      WHERE s.project_id = $1 AND s.sample_key = $2 AND sv.batch_id = $3`,
			fixture.projectID, sampleKey, batchID).Scan(&sampleID, &versionID); err != nil {
			t.Fatalf("resolve sample %s: %v", sampleKey, err)
		}
		versionIDs = append(versionIDs, versionID)
	}

	experiment, err := experiments.CreateExperiment(ctx, CreateExperimentInput{
		ProjectID: fixture.projectID, BatchID: &batchID, TargetKind: model.TargetKindSFT,
		SampleVersionIDs: versionIDs,
		Judges:           []model.JudgeSpec{{ConnectionID: 99999, EndpointFingerprint: "judge.example.com/v1"}},
		Rubric:           comparisonRubric(), CreatedBy: &fixture.userID,
	})
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}

	items, err := experiments.ListExperimentItems(ctx, experiment.ID, "", 10)
	if err != nil {
		t.Fatalf("ListExperimentItems: %v", err)
	}
	for index, item := range items {
		if index >= len(scores) || scores[index] == nil {
			// 缺分：写 missing（不是 0）—— 它不应参与配对均值。
			if _, err := experiments.RecordScore(ctx, RecordScoreInput{
				ExperimentID: experiment.ID, ExperimentItemID: item.ID,
				JudgeConnectionID: 99999, Dimension: "accuracy", ScoreState: model.ScoreStateMissing,
			}); err != nil {
				t.Fatalf("RecordScore(missing): %v", err)
			}
			continue
		}
		if _, err := experiments.RecordScore(ctx, RecordScoreInput{
			ExperimentID: experiment.ID, ExperimentItemID: item.ID,
			JudgeConnectionID: 99999, Dimension: "accuracy",
			RawScore: scores[index], ScoreState: model.ScoreStateScored,
		}); err != nil {
			t.Fatalf("RecordScore: %v", err)
		}
	}
	_ = batches
}

func floatPtr(value float64) *float64 { return &value }

// TestComparisonPairsScoresByUnitKey 覆盖「同一题的两侧评分被对齐」。
func TestComparisonPairsScoresByUnitKey(t *testing.T) {
	fixture := newComparisonFixture(t)
	ctx := context.Background()

	fixture.scoreBatch(t, fixture.leftBatch, []*float64{floatPtr(4), floatPtr(6), floatPtr(5)})
	fixture.scoreBatch(t, fixture.rightBatch, []*float64{floatPtr(8), floatPtr(10), floatPtr(5)})

	baseline, err := fixture.comparisons.CreateComparisonBaseline(ctx, CreateComparisonBaselineInput{
		ProjectID: fixture.projectID, InputRef: "questions-v1",
		Rubric:      comparisonRubric(),
		Judges:      []model.JudgeSpec{{ConnectionID: 99999, EndpointFingerprint: "judge.example.com/v1"}},
		Metric:      model.ComparisonMetricPaired,
		LeftBatchID: fixture.leftBatch, RightBatchID: fixture.rightBatch,
		CreatedBy: &fixture.userID,
	})
	if err != nil {
		t.Fatalf("CreateComparisonBaseline: %v", err)
	}
	if baseline.InputRef == "questions-v1" || !strings.HasPrefix(baseline.InputRef, "sample-input-v1:") {
		t.Fatalf("逐题配对必须使用服务端计算的输入指纹，实际 inputRef=%q", baseline.InputRef)
	}

	report, err := fixture.comparisons.BuildComparisonReport(ctx, fixture.projectID, baseline.ID)
	if err != nil {
		t.Fatalf("BuildComparisonReport: %v", err)
	}
	if report.PairedCount != 3 {
		t.Fatalf("3 个同名单元都应有两侧评分，配对数应为 3，实际 %d", report.PairedCount)
	}
	if !report.Comparability.Comparable {
		t.Fatalf("同基准逐题配对且有配对观测时应可比，实际 %s", report.Comparability.Label)
	}

	// 分数是**归一化后**的（0–10 → 0–1）：左均值 (4+6+5)/3/10=0.5，右 (8+10+5)/3/10≈0.7667。
	dimension := report.Dimensions[0]
	if dimension.Pairs != 3 {
		t.Fatalf("该维度的配对数应为 3，实际 %d", dimension.Pairs)
	}
	if dimension.LeftMean < 0.499 || dimension.LeftMean > 0.501 {
		t.Fatalf("左侧归一化均值应为 0.5，实际 %.4f", dimension.LeftMean)
	}
	if dimension.RightMean < 0.766 || dimension.RightMean > 0.768 {
		t.Fatalf("右侧归一化均值应约为 0.7667，实际 %.4f", dimension.RightMean)
	}
	if dimension.Delta <= 0 {
		t.Fatalf("右侧更高，Delta 应为正数，实际 %.4f", dimension.Delta)
	}
}

// TestComparisonExcludesMissingScores 覆盖「缺分不参与配对也不补 0」。
//
// 补 0 会凭空制造差异：一条缺分的内容若算成 0 分，会让对应一侧看起来差很多。
func TestComparisonExcludesMissingScores(t *testing.T) {
	fixture := newComparisonFixture(t)
	ctx := context.Background()

	// 第 2 个单元只有左侧有分（右侧缺分）。
	fixture.scoreBatch(t, fixture.leftBatch, []*float64{floatPtr(10), floatPtr(10), floatPtr(10)})
	fixture.scoreBatch(t, fixture.rightBatch, []*float64{floatPtr(10), nil, floatPtr(10)})

	baseline, err := fixture.comparisons.CreateComparisonBaseline(ctx, CreateComparisonBaselineInput{
		ProjectID: fixture.projectID, InputRef: "questions-v1", Rubric: comparisonRubric(),
		Judges:      []model.JudgeSpec{{ConnectionID: 99999, EndpointFingerprint: "judge.example.com/v1"}},
		Metric:      model.ComparisonMetricPaired,
		LeftBatchID: fixture.leftBatch, RightBatchID: fixture.rightBatch, CreatedBy: &fixture.userID,
	})
	if err != nil {
		t.Fatalf("CreateComparisonBaseline: %v", err)
	}
	report, err := fixture.comparisons.BuildComparisonReport(ctx, fixture.projectID, baseline.ID)
	if err != nil {
		t.Fatalf("BuildComparisonReport: %v", err)
	}

	if report.PairedCount != 2 {
		t.Fatalf("只有 2 个单元两侧都有分，实际配对 %d", report.PairedCount)
	}
	if report.LeftOnlyCount != 1 {
		t.Fatalf("应有 1 题只在一侧有分，实际 %d", report.LeftOnlyCount)
	}
	dimension := report.Dimensions[0]
	if dimension.LeftMean != 1 || dimension.RightMean != 1 {
		t.Fatalf("均值只能统计配对观测（都应为 1.0），实际 %v / %v", dimension.LeftMean, dimension.RightMean)
	}
	if dimension.Delta != 0 {
		t.Fatalf("配对观测两侧相同，Delta 应为 0（缺分不得被算成 0 分），实际 %v", dimension.Delta)
	}
	if dimension.Missing != 1 {
		t.Fatalf("缺分观测应计为 1，实际 %d", dimension.Missing)
	}
}

// TestComparisonRequiresBothBatchesInProject 覆盖跨项目防护。
func TestComparisonRequiresBothBatchesInProject(t *testing.T) {
	fixture := newComparisonFixture(t)
	ctx := context.Background()

	// 不存在的批次。
	if _, err := fixture.comparisons.CreateComparisonBaseline(ctx, CreateComparisonBaselineInput{
		ProjectID: fixture.projectID, InputRef: "x", Rubric: comparisonRubric(),
		Judges:      []model.JudgeSpec{{ConnectionID: 1, EndpointFingerprint: "j"}},
		Metric:      model.ComparisonMetricPaired,
		LeftBatchID: fixture.leftBatch, RightBatchID: 1 << 62,
	}); !IsStoreValidationError(err) {
		t.Fatalf("不存在的批次必须被拒，实际 %v", err)
	}
	// 不属于本项目。
	other := fixture.projectID + 1_000_000
	if _, err := fixture.comparisons.CreateComparisonBaseline(ctx, CreateComparisonBaselineInput{
		ProjectID: other, InputRef: "x", Rubric: comparisonRubric(),
		Judges:      []model.JudgeSpec{{ConnectionID: 1, EndpointFingerprint: "j"}},
		Metric:      model.ComparisonMetricPaired,
		LeftBatchID: fixture.leftBatch, RightBatchID: fixture.rightBatch,
	}); !IsStoreValidationError(err) {
		t.Fatalf("跨项目批次必须被拒，实际 %v", err)
	}
	// 两侧相同（判据层已挡，这里确认它确实到达判据）。
	if _, err := fixture.comparisons.CreateComparisonBaseline(ctx, CreateComparisonBaselineInput{
		ProjectID: fixture.projectID, InputRef: "x", Rubric: comparisonRubric(),
		Judges:      []model.JudgeSpec{{ConnectionID: 1, EndpointFingerprint: "j"}},
		Metric:      model.ComparisonMetricPaired,
		LeftBatchID: fixture.leftBatch, RightBatchID: fixture.leftBatch,
	}); err == nil {
		t.Fatal("两侧相同必须被拒（与自己比较的差异恒为 0）")
	}
}

// TestComparisonRejectsDifferentInputFingerprint 确认 paired 不信任客户端传入的
// inputRef：只要两侧实际题面不同，即使客户端伪造了同一个字符串也必须拒绝。
func TestComparisonRejectsDifferentInputFingerprint(t *testing.T) {
	fixture := newComparisonFixture(t)
	ctx := context.Background()

	// 右侧同一稳定单元重新生成了不同题面；服务端应以最新版本为准发现漂移。
	if _, _, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: "cold-chain/temperature#1",
		TargetKind: model.TargetKindSFT, Title: "cold-chain/temperature#1",
		BatchID: &fixture.rightBatch,
		Payload:  map[string]any{"question": "changed-input", "reasoning": "r", "answer": "a"},
	}); err != nil {
		t.Fatalf("AppendSampleVersion: %v", err)
	}

	if _, err := fixture.comparisons.CreateComparisonBaseline(ctx, CreateComparisonBaselineInput{
		ProjectID: fixture.projectID, InputRef: "same-input-via-client",
		Rubric: comparisonRubric(),
		Judges: []model.JudgeSpec{{ConnectionID: 99999, EndpointFingerprint: "judge.example.com/v1"}},
		Metric: model.ComparisonMetricPaired,
		LeftBatchID: fixture.leftBatch, RightBatchID: fixture.rightBatch,
		CreatedBy: &fixture.userID,
	}); err == nil {
		t.Fatal("两侧题面不同，即使客户端伪造 inputRef 也必须拒绝")
	}
}

// TestAdoptUpdatesPointerAndKeepsEvidence 覆盖验收项
// 「采用 A/B 只更新项目采用指针并记录依据」。
func TestAdoptUpdatesPointerAndKeepsEvidence(t *testing.T) {
	fixture := newComparisonFixture(t)
	ctx := context.Background()
	fixture.scoreBatch(t, fixture.leftBatch, []*float64{floatPtr(4), floatPtr(5), floatPtr(6)})
	fixture.scoreBatch(t, fixture.rightBatch, []*float64{floatPtr(8), floatPtr(9), floatPtr(10)})

	baseline, err := fixture.comparisons.CreateComparisonBaseline(ctx, CreateComparisonBaselineInput{
		ProjectID: fixture.projectID, InputRef: "questions-v1", Rubric: comparisonRubric(),
		Judges:      []model.JudgeSpec{{ConnectionID: 99999, EndpointFingerprint: "judge.example.com/v1"}},
		Metric:      model.ComparisonMetricPaired,
		LeftBatchID: fixture.leftBatch, RightBatchID: fixture.rightBatch, CreatedBy: &fixture.userID,
	})
	if err != nil {
		t.Fatalf("CreateComparisonBaseline: %v", err)
	}

	// 缺理由 → 拒绝（没有依据的决定无法在以后复核）。
	if _, _, err := fixture.comparisons.Adopt(ctx, AdoptComparisonInput{
		ProjectID: fixture.projectID, BaselineID: baseline.ID, Side: "right",
		Reason: "  ", CreatedBy: &fixture.userID,
	}); err == nil {
		t.Fatal("缺理由必须被拒绝")
	}

	_, adoptedBatch, err := fixture.comparisons.Adopt(ctx, AdoptComparisonInput{
		ProjectID: fixture.projectID, BaselineID: baseline.ID, Side: "right",
		Reason: "右侧在准确维度更好且成本相近", CreatedBy: &fixture.userID,
	})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if adoptedBatch != fixture.rightBatch {
		t.Fatalf("采用的应是右侧批次 %d，实际 %d", fixture.rightBatch, adoptedBatch)
	}

	// 指针已更新。
	batchID, baselineID, side, found, err := fixture.comparisons.AdoptedBatch(ctx, fixture.projectID)
	if err != nil || !found {
		t.Fatalf("采用后应有指针：found=%v err=%v", found, err)
	}
	if batchID != fixture.rightBatch || baselineID != baseline.ID || side != "right" {
		t.Fatalf("指针内容不对：batch=%d baseline=%d side=%s", batchID, baselineID, side)
	}

	// 依据被冻结保存（只追加）。
	adoptions, err := fixture.comparisons.ListAdoptions(ctx, fixture.projectID, 10)
	if err != nil {
		t.Fatalf("ListAdoptions: %v", err)
	}
	if len(adoptions) != 1 {
		t.Fatalf("应有 1 条采用记录，实际 %d", len(adoptions))
	}
	if !strings.Contains(fmt.Sprint(adoptions[0]["reason"]), "准确维度") {
		t.Fatalf("依据必须保留，实际 %v", adoptions[0]["reason"])
	}

	// 已采用后可再次采用（改方案要重新比较）—— 但指针只保留最新一条，
	// 历史仍可查（这就是「只追加 + 一行指针」的分工）。
	if _, _, err := fixture.comparisons.Adopt(ctx, AdoptComparisonInput{
		ProjectID: fixture.projectID, BaselineID: baseline.ID, Side: "left",
		Reason: "复核后发现左侧更稳定", CreatedBy: &fixture.userID,
	}); err != nil {
		t.Fatalf("再次采用: %v", err)
	}
	adoptions, _ = fixture.comparisons.ListAdoptions(ctx, fixture.projectID, 10)
	if len(adoptions) != 2 {
		t.Fatalf("两条采用依据都应保留，实际 %d", len(adoptions))
	}
	batchID, _, side, _, _ = fixture.comparisons.AdoptedBatch(ctx, fixture.projectID)
	if batchID != fixture.leftBatch || side != "left" {
		t.Fatalf("指针应指向最新采用的左侧，实际 batch=%d side=%s", batchID, side)
	}
}

var _ = errors.Is
