package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T14 的**实验冻结**语义。
//
// 必须连真实 Postgres：核心断言都是数据库语义 —— 冻结靠的是「创建时写入的
// 行 + 快照 JSONB 不随后续配置变化」，分母固定靠 RESTRICT 外键与独立表，
// 缺分靠可空列。内存实现「测」的话测的是测试自己的实现。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（T32 会检查这些测试不是 Skip）。

type experimentFixture struct {
	pool          *pgxpool.Pool
	projectID     int64
	userID        int64
	generatorConn int64
	experiments   *ExperimentStore
}

// newExperimentFixture 建项目 + 两个生成连接（其中一个与裁判同源）+ 3 个样本版本。
func newExperimentFixture(t *testing.T) experimentFixture {
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
		"实验测试工作区 "+suffix, "experiment-test-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user')
    ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role RETURNING id`,
		"experiment-"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	projects := NewProjectStore(pool)
	input := model.CreateProjectInput{Name: "实验项目", TargetKind: model.TargetKindSFT}
	input.Normalize()
	project, err := projects.CreateProject(ctx, workspaceID, userID, input)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// 两个连接：generator（api.vendor.com）与 judge（judge.example.com）。
	generatorConn := seedConnection(t, pool, "generator-"+suffix, "https://api.vendor.com/v1")
	judgeConn := seedConnection(t, pool, "judge-"+suffix, "https://judge.example.com/v1")
	aliasConn := seedConnection(t, pool, "alias-"+suffix, "https://api.vendor.com/v1")

	// 批次持有 generator 连接（生成来源由它推导）。
	//
	// 用 json.Marshal 而不是手拼 JSON：手拼既可能格式错误，
	// 又会让静态分析器无法区分「参数值」与「被拼进 SQL 的文本」——
	// 真正的注入点会被淹没在噪声里（与本仓库既有的同类取舍一致）。
	generationConfig, err := json.Marshal(map[string]any{
		"modelConnectionId": generatorConn,
		"concurrency":       1,
		"maxTokens":         512,
		"schemaVersion":     "sft.sample.v1",
	})
	if err != nil {
		t.Fatalf("marshal generation config: %v", err)
	}
	var batchID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO batches (project_id, purpose, status, control_state, target_kind, schema_version,
                         generation_config, planned_units, budget_currency, created_by)
    VALUES ($1, 'pilot', 'completed', 'run', 'sft', 'sft.sample.v1',
            $2::jsonb, 3, 'CNY', $3) RETURNING id`,
		project.ID, generationConfig, userID).Scan(&batchID); err != nil {
		t.Fatalf("seed batch: %v", err)
	}

	// 3 个样本版本（带内容 hash 与批次身份）。
	for index := 1; index <= 3; index++ {
		var sampleID int64
		if err := pool.QueryRow(ctx, `
      INSERT INTO samples (project_id, sample_key, target_kind, title, latest_version)
      VALUES ($1, $2, 'sft', $2, 1) RETURNING id`,
			project.ID, "item-"+strconv.Itoa(index)+"-"+suffix).Scan(&sampleID); err != nil {
			t.Fatalf("seed sample: %v", err)
		}
		if _, err := pool.Exec(ctx, `
      INSERT INTO sample_versions
        (sample_id, project_id, version, target_kind, schema_version, payload, content_hash, batch_id)
      VALUES ($1, $2, 1, 'sft', 'sft.sample.v1', '{"question":"q","reasoning":"r","answer":"a"}'::jsonb, $3, $4)`,
			sampleID, project.ID, "hash-"+strconv.Itoa(index)+"-"+suffix, batchID); err != nil {
			t.Fatalf("seed sample version: %v", err)
		}
	}

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM experiments WHERE project_id = $1`, project.ID)
		_, _ = pool.Exec(bg, `DELETE FROM projects WHERE workspace_id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM model_providers WHERE id = ANY($1::bigint[])`,
			[]int64{generatorConn, judgeConn, aliasConn})
	})

	return experimentFixture{pool: pool, projectID: project.ID, userID: userID,
		generatorConn: generatorConn, experiments: NewExperimentStore(pool)}
}

func seedConnection(t *testing.T, pool *pgxpool.Pool, name, baseURL string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
    INSERT INTO model_providers (name, base_url, model, api_key_masked)
    VALUES ($1, $2, 'm', '') RETURNING id`, name, baseURL).Scan(&id); err != nil {
		t.Fatalf("seed connection %s: %v", name, err)
	}
	return id
}

// sampleVersionIDs 返回本项目的样本版本 ID（升序）。
func (fixture experimentFixture) sampleVersionIDs(t *testing.T) []int64 {
	t.Helper()
	rows, err := fixture.pool.Query(context.Background(), `
    SELECT id FROM sample_versions WHERE project_id = $1 ORDER BY id`, fixture.projectID)
	if err != nil {
		t.Fatalf("list sample versions: %v", err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func validRubric() model.RubricSpec {
	return model.RubricSpec{Dimensions: []model.RubricDimension{
		{Key: "accuracy", Label: "准确", Weight: 0.6, Min: 0, Max: 10},
		{Key: "reasoning", Label: "推理", Weight: 0.4, Min: 0, Max: 10},
	}}
}

func (fixture experimentFixture) createExperiment(t *testing.T, judges []model.JudgeSpec) model.Experiment {
	t.Helper()
	experiment, err := fixture.experiments.CreateExperiment(context.Background(), CreateExperimentInput{
		ProjectID: fixture.projectID, TargetKind: model.TargetKindSFT,
		SampleVersionIDs: fixture.sampleVersionIDs(t),
		Judges:           judges, Rubric: validRubric(), CreatedBy: &fixture.userID,
	})
	if err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	return experiment
}

// TestCreateExperimentFreezesSnapshot 覆盖验收项
// 「入队后修改维度/provider/样本当前指针不改变实验」。
//
// 这是本任务最重要的一条：报告是用来支撑「可以发布」这个决定的，
// 因此它引用的配置与范围必须**冻结**，否则管理员改一次量表权重，
// 所有历史报告的结论都会跟着变。
func TestCreateExperimentFreezesSnapshot(t *testing.T) {
	fixture := newExperimentFixture(t)
	ctx := context.Background()

	judges := []model.JudgeSpec{{ConnectionID: fixture.judgeConn(t), Label: "独立裁判",
		EndpointFingerprint: "judge.example.com/v1"}}
	experiment := fixture.createExperiment(t, judges)

	// 破坏性变更：改 provider 的 base_url、把样本推进到新版本并追加新版本。
	// 只改**本 fixture 的生成连接**：改全表会污染同库里其它测试
	//（例如 T07 的价格解析测试），而那种污染只在特定执行顺序下才暴露。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE model_providers SET base_url = 'https://changed.example.com/v9' WHERE id = $1`,
		fixture.generatorConn); err != nil {
		t.Fatalf("change provider: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE samples SET latest_version = 2, updated_at = NOW() WHERE project_id = $1`, fixture.projectID); err != nil {
		t.Fatalf("advance sample pointer: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
    INSERT INTO sample_versions
      (sample_id, project_id, version, target_kind, schema_version, payload, content_hash)
    SELECT sample_id, project_id, 2, target_kind, schema_version, payload, content_hash || '-v2'
    FROM sample_versions WHERE project_id = $1 AND version = 1`, fixture.projectID); err != nil {
		t.Fatalf("append new version: %v", err)
	}

	reloaded, err := fixture.experiments.GetExperiment(ctx, experiment.ID)
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	// 量表快照不变。
	if len(reloaded.Rubric.Dimensions) != 2 || reloaded.Rubric.Dimensions[0].Weight != 0.6 {
		t.Fatalf("量表快照必须冻结，实际 %+v", reloaded.Rubric.Dimensions)
	}
	// 分母不变（新版本没有被纳入）。
	if reloaded.InspectedCount != 3 {
		t.Fatalf("分母必须冻结为 3，实际 %d", reloaded.InspectedCount)
	}
	items, err := fixture.experiments.ListExperimentItems(ctx, experiment.ID, "", 10)
	if err != nil {
		t.Fatalf("ListExperimentItems: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("实验范围必须固定为 3 项，实际 %d（样本新版本不应被自动纳入）", len(items))
	}
	// 生成来源指纹冻结（provider base_url 改了也不影响历史判定）。
	if len(reloaded.GeneratorSources) == 0 || reloaded.GeneratorSources[0].EndpointFingerprint == "" {
		t.Fatalf("生成来源指纹必须冻结，实际 %+v", reloaded.GeneratorSources)
	}
	if strings.Contains(reloaded.GeneratorSources[0].EndpointFingerprint, "changed.example.com") {
		t.Fatal("生成来源指纹不得随 provider 当前配置漂移")
	}
}

// TestMissingScoreIsNotZero 覆盖验收项「缺分/error/不适用不等于 0」。
func TestMissingScoreIsNotZero(t *testing.T) {
	fixture := newExperimentFixture(t)
	ctx := context.Background()
	experiment := fixture.createExperiment(t, []model.JudgeSpec{
		{ConnectionID: fixture.judgeConn(t), EndpointFingerprint: "judge.example.com/v1"},
	})
	items, err := fixture.experiments.ListExperimentItems(ctx, experiment.ID, "", 10)
	if err != nil {
		t.Fatalf("ListExperimentItems: %v", err)
	}

	score := 8.0
	// 第 1 项正常打分；第 2 项缺分；第 3 项出错。
	if _, err := fixture.experiments.RecordScore(ctx, RecordScoreInput{
		ExperimentID: experiment.ID, ExperimentItemID: items[0].ID,
		JudgeConnectionID: experiment.Judges[0].ConnectionID, Dimension: "accuracy",
		RawScore: &score, ScoreState: model.ScoreStateScored,
	}); err != nil {
		t.Fatalf("RecordScore scored: %v", err)
	}
	if _, err := fixture.experiments.RecordScore(ctx, RecordScoreInput{
		ExperimentID: experiment.ID, ExperimentItemID: items[1].ID,
		JudgeConnectionID: experiment.Judges[0].ConnectionID, Dimension: "accuracy",
		ScoreState: model.ScoreStateMissing,
	}); err != nil {
		t.Fatalf("RecordScore missing: %v", err)
	}
	if _, err := fixture.experiments.RecordScore(ctx, RecordScoreInput{
		ExperimentID: experiment.ID, ExperimentItemID: items[2].ID,
		JudgeConnectionID: experiment.Judges[0].ConnectionID, Dimension: "accuracy",
		ScoreState: model.ScoreStateError, ErrorClass: model.ErrorClassTimeout,
	}); err != nil {
		t.Fatalf("RecordScore error: %v", err)
	}

	// 缺分/出错时**不得**给出分数（否则缺分会被算成 0 分）。
	if _, err := fixture.experiments.RecordScore(ctx, RecordScoreInput{
		ExperimentID: experiment.ID, ExperimentItemID: items[1].ID,
		JudgeConnectionID: experiment.Judges[0].ConnectionID, Dimension: "reasoning",
		RawScore: &score, ScoreState: model.ScoreStateMissing,
	}); !IsStoreValidationError(err) {
		t.Fatalf("缺分却给分数必须被拒，实际 %v", err)
	}
	// 标记已评分却没给分数同样必须被拒。
	if _, err := fixture.experiments.RecordScore(ctx, RecordScoreInput{
		ExperimentID: experiment.ID, ExperimentItemID: items[1].ID,
		JudgeConnectionID: experiment.Judges[0].ConnectionID, Dimension: "reasoning",
		ScoreState: model.ScoreStateScored,
	}); !IsStoreValidationError(err) {
		t.Fatalf("标为已评分却没分数必须被拒，实际 %v", err)
	}

	// 项状态：第 1 项 scored，第 2 项 missing，第 3 项 error。
	for index, status := range []string{model.ExperimentItemScored, model.ExperimentItemMissing, model.ExperimentItemError} {
		if _, err := fixture.experiments.MarkExperimentItemStatus(ctx, items[index].ID, status, "", ""); err != nil {
			t.Fatalf("MarkExperimentItemStatus(%s): %v", status, err)
		}
	}
	refreshed, err := fixture.experiments.RefreshExperimentCounts(ctx, experiment.ID)
	if err != nil {
		t.Fatalf("RefreshExperimentCounts: %v", err)
	}
	if refreshed.InspectedCount != 3 {
		t.Fatalf("分母必须固定为 3（缺分/出错不缩小分母），实际 %d", refreshed.InspectedCount)
	}
	if refreshed.ScoredCount != 1 || refreshed.MissingCount != 1 || refreshed.ErrorCount != 1 {
		t.Fatalf("三个侧面计数应各为 1，实际 scored=%d missing=%d error=%d",
			refreshed.ScoredCount, refreshed.MissingCount, refreshed.ErrorCount)
	}
	if refreshed.Status != model.ExperimentStatusPartialFailed {
		t.Fatalf("有出错项且无 pending 时应为 partial_failed，实际 %s", refreshed.Status)
	}

	// 报告：accuracy 维度的平均分只由**已评分**的那一格决定（8/10=0.8），
	// 缺分与出错**不参与**，也不被当成 0（那会把平均分拉到 0.267）。
	report, err := fixture.experiments.BuildExperimentReport(ctx, experiment.ID)
	if err != nil {
		t.Fatalf("BuildExperimentReport: %v", err)
	}
	if report.Stats.Inspected != 3 {
		t.Fatalf("报告分母必须为 3，实际 %d", report.Stats.Inspected)
	}
	var accuracy *ExperimentDimensionStat
	for index := range report.Dimensions {
		if report.Dimensions[index].Dimension == "accuracy" {
			accuracy = &report.Dimensions[index]
		}
	}
	if accuracy == nil {
		t.Fatalf("报告必须包含 accuracy 维度，实际 %+v", report.Dimensions)
	}
	if accuracy.Mean < 0.79 || accuracy.Mean > 0.81 {
		t.Fatalf("缺分不得被算成 0 分：accuracy 均值应为 0.8，实际 %.3f", accuracy.Mean)
	}
	if accuracy.ScoredCount != 1 || accuracy.MissingCount != 2 {
		t.Fatalf("覆盖必须显式给出：scored=%d missing=%d", accuracy.ScoredCount, accuracy.MissingCount)
	}
	if !strings.Contains(strings.Join(report.JudgeNotes, " "), "覆盖") {
		t.Fatalf("未满覆盖必须在报告里说明（不能声称全量通过），实际 %v", report.JudgeNotes)
	}
}

// TestResumeDoesNotOverwriteScoredItems 覆盖验收项
// 「失败项续跑不覆盖成功证据」。
func TestResumeDoesNotOverwriteScoredItems(t *testing.T) {
	fixture := newExperimentFixture(t)
	ctx := context.Background()
	experiment := fixture.createExperiment(t, []model.JudgeSpec{
		{ConnectionID: fixture.judgeConn(t), EndpointFingerprint: "judge.example.com/v1"},
	})
	items, _ := fixture.experiments.ListExperimentItems(ctx, experiment.ID, "", 10)

	if _, err := fixture.experiments.MarkExperimentItemStatus(ctx, items[0].ID, model.ExperimentItemScored, "", ""); err != nil {
		t.Fatalf("mark scored: %v", err)
	}
	// 迟到的失败上报**不得**把已评分变成出错（否则报告的分子会变小）。
	changed, err := fixture.experiments.MarkExperimentItemStatus(ctx, items[0].ID,
		model.ExperimentItemError, model.ErrorClassTimeout, "迟到的超时上报")
	if err != nil {
		t.Fatalf("late failure: %v", err)
	}
	if changed {
		t.Fatal("已评分的项不得被迟到的失败改写（成功证据必须保留）")
	}
	reloaded, err := fixture.experiments.ListExperimentItems(ctx, experiment.ID, "", 10)
	if err != nil {
		t.Fatalf("ListExperimentItems: %v", err)
	}
	if reloaded[0].Status != model.ExperimentItemScored {
		t.Fatalf("第 1 项必须仍是 scored，实际 %s", reloaded[0].Status)
	}

	// 续跑清单只含未完成项（scored 不在其中）。
	pending, err := fixture.experiments.ListPendingExperimentItems(ctx, experiment.ID, 10)
	if err != nil {
		t.Fatalf("ListPendingExperimentItems: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("续跑清单应只含 2 个未完成项，实际 %d", len(pending))
	}
	for _, item := range pending {
		if item.Status == model.ExperimentItemScored {
			t.Fatal("续跑清单不得包含已评分项")
		}
	}
}

// TestCreateExperimentRejectsSelfJudgingAndGRPO 覆盖两条拒绝路径。
func TestCreateExperimentRejectsSelfJudgingAndGRPO(t *testing.T) {
	fixture := newExperimentFixture(t)
	ctx := context.Background()

	// 只有与生成来源同源的别名连接 → 拒绝（自评）。
	_, err := fixture.experiments.CreateExperiment(ctx, CreateExperimentInput{
		ProjectID: fixture.projectID, TargetKind: model.TargetKindSFT,
		SampleVersionIDs: fixture.sampleVersionIDs(t),
		Judges: []model.JudgeSpec{{ConnectionID: fixture.aliasConn(t),
			EndpointFingerprint: "api.vendor.com/v1"}},
		Rubric: validRubric(),
	})
	if !IsStoreValidationError(err) {
		t.Fatalf("只有别名连接时必须拒绝（否则是自评），实际 %v", err)
	}

	// GRPO 现在可运行（T24），但**量表必须匹配目标类型**：
	// 用 SFT 量表评 GRPO 样本会产出「看起来正常、其实语义错误」的结论，
	// 而用 GRPO 量表评 SFT 样本会让确定性维度永远缺分。
	_, err = fixture.experiments.CreateExperiment(ctx, CreateExperimentInput{
		ProjectID: fixture.projectID, TargetKind: model.TargetKindGRPO,
		SampleVersionIDs: fixture.sampleVersionIDs(t),
		Judges: []model.JudgeSpec{{ConnectionID: fixture.judgeConn(t),
			EndpointFingerprint: "judge.example.com/v1"}},
		Rubric: validRubric(),
	})
	if err == nil || !strings.Contains(err.Error(), "GRPO") {
		t.Fatalf("错配的量表必须被拒绝并点明 GRPO，实际 %v", err)
	}

	// 用内置 GRPO 量表则应当接受（T24 的可用路径）。
	grpoExperiment, err := fixture.experiments.CreateExperiment(ctx, CreateExperimentInput{
		ProjectID: fixture.projectID, TargetKind: model.TargetKindGRPO,
		SampleVersionIDs: fixture.sampleVersionIDs(t),
		Judges: []model.JudgeSpec{{ConnectionID: fixture.judgeConn(t),
			EndpointFingerprint: "judge.example.com/v1"}},
		Rubric: model.BuiltinGRPORubric(),
	})
	if err != nil {
		t.Fatalf("GRPO 内置量表应当被接受：%v", err)
	}
	if grpoExperiment.TargetKind != model.TargetKindGRPO {
		t.Fatalf("目标类型应为 grpo，实际 %s", grpoExperiment.TargetKind)
	}
	if len(grpoExperiment.Rubric.Dimensions) != 3 {
		t.Fatalf("GRPO 实验应冻结三个内置维度，实际 %d", len(grpoExperiment.Rubric.Dimensions))
	}

	// 空范围必须拒绝（否则分母为 0 却声称有结论）。
	_, err = fixture.experiments.CreateExperiment(ctx, CreateExperimentInput{
		ProjectID: fixture.projectID, TargetKind: model.TargetKindSFT,
		Judges: []model.JudgeSpec{{ConnectionID: fixture.judgeConn(t),
			EndpointFingerprint: "judge.example.com/v1"}},
		Rubric: validRubric(),
	})
	if !IsStoreValidationError(err) {
		t.Fatalf("空范围必须被拒绝，实际 %v", err)
	}

	// 范围里含不属于本项目的版本必须拒绝（避免分母静默变小）。
	_, err = fixture.experiments.CreateExperiment(ctx, CreateExperimentInput{
		ProjectID: fixture.projectID, TargetKind: model.TargetKindSFT,
		SampleVersionIDs: append(fixture.sampleVersionIDs(t), 1<<62),
		Judges: []model.JudgeSpec{{ConnectionID: fixture.judgeConn(t),
			EndpointFingerprint: "judge.example.com/v1"}},
		Rubric: validRubric(),
	})
	if !IsStoreValidationError(err) {
		t.Fatalf("范围含不存在的版本必须被拒绝，实际 %v", err)
	}
}

// TestExperimentItemsProtectSampleVersions 覆盖「分母不可被做小」。
//
// 直接删除已进入实验的样本版本必须失败（ON DELETE RESTRICT）：
// CASCADE 会让「删一个样本」静默缩小历史实验的分母。
func TestExperimentItemsProtectSampleVersions(t *testing.T) {
	fixture := newExperimentFixture(t)
	ctx := context.Background()
	fixture.createExperiment(t, []model.JudgeSpec{
		{ConnectionID: fixture.judgeConn(t), EndpointFingerprint: "judge.example.com/v1"},
	})

	_, err := fixture.pool.Exec(ctx, `DELETE FROM sample_versions WHERE project_id = $1`, fixture.projectID)
	if err == nil {
		t.Fatal("已进入实验的样本版本不得被删除（分母不可被做小）")
	}
}

// TestRecordScoreSupersedesWithoutLosingHistory 覆盖「更正保留历史」。
func TestRecordScoreSupersedesWithoutLosingHistory(t *testing.T) {
	fixture := newExperimentFixture(t)
	ctx := context.Background()
	experiment := fixture.createExperiment(t, []model.JudgeSpec{
		{ConnectionID: fixture.judgeConn(t), EndpointFingerprint: "judge.example.com/v1"},
	})
	items, _ := fixture.experiments.ListExperimentItems(ctx, experiment.ID, "", 10)

	first := 5.0
	second := 9.0
	if _, err := fixture.experiments.RecordScore(ctx, RecordScoreInput{
		ExperimentID: experiment.ID, ExperimentItemID: items[0].ID,
		JudgeConnectionID: experiment.Judges[0].ConnectionID, Dimension: "accuracy", RawScore: &first,
	}); err != nil {
		t.Fatalf("first score: %v", err)
	}
	if _, err := fixture.experiments.RecordScore(ctx, RecordScoreInput{
		ExperimentID: experiment.ID, ExperimentItemID: items[0].ID,
		JudgeConnectionID: experiment.Judges[0].ConnectionID, Dimension: "accuracy", RawScore: &second,
	}); err != nil {
		t.Fatalf("superseding score: %v", err)
	}

	// 两行都在（历史不丢），但只有一行是当前有效。
	var total, current int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*), COUNT(*) FILTER (WHERE superseded_by IS NULL)
    FROM experiment_scores WHERE experiment_item_id = $1 AND dimension = 'accuracy'`,
		items[0].ID).Scan(&total, &current); err != nil {
		t.Fatalf("count scores: %v", err)
	}
	if total != 2 || current != 1 {
		t.Fatalf("更正必须保留历史且只有一行有效，实际 total=%d current=%d", total, current)
	}

	// 报告只用当前有效行（9/10=0.9），不把历史分也算进去。
	report, err := fixture.experiments.BuildExperimentReport(ctx, experiment.ID)
	if err != nil {
		t.Fatalf("BuildExperimentReport: %v", err)
	}
	for _, dimension := range report.Dimensions {
		if dimension.Dimension == "accuracy" {
			if dimension.Mean < 0.89 || dimension.Mean > 0.91 {
				t.Fatalf("报告必须只用当前有效评分（0.9），实际 %.3f", dimension.Mean)
			}
			if dimension.ScoredCount != 1 {
				t.Fatalf("被取代的行不得计入覆盖，实际 %d", dimension.ScoredCount)
			}
		}
	}
}

func (fixture experimentFixture) judgeConn(t *testing.T) int64 {
	return fixture.connectionIDNamed(t, "judge-")
}
func (fixture experimentFixture) aliasConn(t *testing.T) int64 {
	return fixture.connectionIDNamed(t, "alias-")
}

func (fixture experimentFixture) connectionIDNamed(t *testing.T, prefix string) int64 {
	t.Helper()
	var id int64
	if err := fixture.pool.QueryRow(context.Background(), `
    SELECT id FROM model_providers WHERE name LIKE $1 ORDER BY id DESC LIMIT 1`,
		prefix+"%").Scan(&id); err != nil {
		t.Fatalf("resolve connection %s: %v", prefix, err)
	}
	return id
}
